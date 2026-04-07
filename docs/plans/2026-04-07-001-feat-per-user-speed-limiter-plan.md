---
title: "feat: Per-User Speed Limiter Service"
type: feat
status: active
date: 2026-04-07
---

# feat: Per-User Speed Limiter Service

## Overview

为 sing-box 添加一个独立的 `speed-limiter` 服务，实现跨协议的用户级限速。支持按用户、用户分组、时间段进行条件限速，上行/下行独立配置。不论用户有多少并发连接，限速作用于该用户的所有连接总和。

## Problem Frame

当前 sing-box 的用户系统（user-provider）可以管理用户的认证和动态加载，但没有任何流量控制能力。运营场景中需要对不同用户/套餐/时段实施差异化的带宽限制。现有的 v2ray stats 只做流量计数，不做限速。Hysteria/Hysteria2 的 `UpMbps`/`DownMbps` 是协议级拥塞控制参数，不是应用层用户限速。

## Requirements Trace

- R1. 对每个用户的所有连接（TCP + UDP）进行聚合限速，不论连接数量
- R2. 上行（upload）和下行（download）独立配置限速值，单位 Mbps
- R3. 支持用户分组（group），同一分组的用户共享相同限速策略
- R4. 支持时间段条件限速（如高峰/低峰时段不同速率）
- R5. 支持按用户名覆盖分组默认限速（per-user override）
- R6. 作为独立服务（service type）注册，与 user-provider 解耦
- R7. 通过 `ConnectionTracker` 接口注入路由层，对所有协议透明生效
- R8. 限速算法使用令牌桶（token bucket），高效且支持突发

## Scope Boundaries

- **不做**: 连接级限速（per-connection），只做用户级聚合
- **不做**: 基于流量配额的断连（如月度流量限额用完后断开）
- **不做**: 动态 API 修改限速规则（可作为后续扩展）
- **不做**: outbound 级别的全局限速（只做用户维度）
- **不做**: 与 user-provider 的数据源集成（限速规则通过自身配置定义）

## Context & Research

### Relevant Code and Patterns

- `adapter/router.go:33-36` — `ConnectionTracker` 接口，限速服务的注入点
- `experimental/v2rayapi/stats.go` — `StatsService` 实现 `ConnectionTracker` 的参考模式，使用 `bufio.NewInt64CounterConn` 包装连接
- `box.go:351,361` — `router.AppendTracker()` 的注册方式
- `adapter/user_provider.go` — `adapter.User` 结构体，`metadata.User` 的来源
- `adapter/inbound.go:50` — `InboundContext.User` 字段，在路由层传递用户身份
- `route/route.go:156-158` — tracker 包装连接的执行位置（rule matching 之后、outbound 之前）
- `constant/speed.go:3` — `MbpsToBps = 125000` 常量
- `service/userprovider/service.go` — 服务注册模式参考（`boxService.Register[Options]`）
- `include/registry.go` — `ServiceRegistry()` 中服务的注册入口

### External References

- `golang.org/x/time/rate` — 标准令牌桶实现，已作为间接依赖存在于 go.mod（v0.11.0）
- Token bucket 算法: 允许突发（burst），平滑限速，适合网络流量控制

## Key Technical Decisions

- **独立服务 vs 嵌入 user-provider**: 选择独立服务。限速逻辑与用户认证/加载是正交关注点，解耦后各自可独立演进。限速服务通过 `metadata.User` 关联用户，不需要直接依赖 user-provider。
- **令牌桶 vs 漏桶 vs 滑动窗口**: 选择令牌桶（`golang.org/x/time/rate`）。令牌桶天然支持突发流量，API 简洁，`rate.Limiter.WaitN()` 直接阻塞到令牌可用，适合 conn Read/Write 包装。已作为间接依赖，无需新增。
- **限速粒度**: 按用户名聚合。同一用户名的所有连接共享同一个 `rate.Limiter` 实例。
- **连接包装方式**: 自定义 `ThrottledConn` / `ThrottledPacketConn`，在 `Read`/`Write` 中调用 `limiter.WaitN(ctx, n)` 实现阻塞式限速。不复用 `bufio.NewCounterConn`（它只计数不限速）。
- **时间段切换**: 使用定时器周期检查当前时间，匹配到不同时段时替换 limiter 的速率（`limiter.SetLimit()` + `limiter.SetBurst()`），已有连接自动生效，无需重新包装。
- **用户分组**: 在限速服务的配置中定义 groups，每个 group 有默认限速；users 配置可指定 group 或直接覆盖限速值。

## Open Questions

### Resolved During Planning

- **Q: 限速应该在 tracker 链的什么位置？** A: 在 stats tracker 之前或之后均可，因为 stats 只计数不修改流量。建议在 stats 之后（后注册先执行不适用，`AppendTracker` 是顺序执行），让 stats 统计的是限速前的真实连接速率。实际上顺序影响不大，因为限速只是延迟不丢弃。
- **Q: 令牌桶的 burst 值怎么设？** A: 默认 burst = 1 秒的速率对应字节数（即 `uploadMbps * MbpsToBps`），允许 1 秒的突发。可通过配置覆盖。
- **Q: 用户不在限速规则中时怎么处理？** A: 不限速，直接返回原始连接（与 StatsService 的 "不在列表中则跳过" 模式一致）。

### Deferred to Implementation

- `ThrottledConn` 的精确接口设计（是否需要实现 `N.ExtendedConn` 的所有方法）
- Read/Write 中 `WaitN` 的 context 取消行为细节
- UDP packet conn 的 burst 大小是否需要与 TCP 不同

## High-Level Technical Design

> *This illustrates the intended approach and is directional guidance for review, not implementation specification. The implementing agent should treat it as context, not code to reproduce.*

```
                     ┌─────────────────────────┐
                     │   speed-limiter config   │
                     │  ┌─────────────────────┐ │
                     │  │ groups:              │ │
                     │  │   - name: premium    │ │
                     │  │     upload: 100      │ │
                     │  │     download: 200    │ │
                     │  │ users:               │ │
                     │  │   - name: alice      │ │
                     │  │     group: premium   │ │
                     │  │   - name: bob        │ │
                     │  │     upload: 10       │ │
                     │  │     download: 20     │ │
                     │  │ schedules:           │ │
                     │  │   - time: 18:00-23:00│ │
                     │  │     upload: 50       │ │
                     │  └─────────────────────┘ │
                     └────────────┬────────────┘
                                  │
                                  ▼
┌──────────┐    ┌──────────┐    ┌──────────────────┐
│ Inbound  │───▶│  Router  │───▶│  SpeedLimiter    │
│ protocol │    │  rule    │    │  (ConnTracker)   │
│          │    │  match   │    │                  │
│ metadata │    │          │    │ metadata.User    │
│  .User   │    │          │    │   ──▶ lookup     │
│  = "bob" │    │          │    │   ──▶ limiter    │
└──────────┘    └──────────┘    │   ──▶ wrap conn  │
                                └────────┬─────────┘
                                         │
                                         ▼
                                ┌──────────────────┐
                                │ ThrottledConn     │
                                │  Read() {         │
                                │    n = inner.Read()│
                                │    dl.WaitN(n)    │
                                │  }                │
                                │  Write() {        │
                                │    ul.WaitN(n)    │
                                │    inner.Write()  │
                                │  }                │
                                └────────┬─────────┘
                                         │
                                         ▼
                                ┌──────────────────┐
                                │    Outbound      │
                                └──────────────────┘
```

**Limiter 生命周期:**
- 服务启动时根据配置为每个用户/分组创建 `rate.Limiter` 实例
- 同一用户的所有连接共享同一对 limiter（upload + download）
- 时间段切换时通过 `SetLimit()`/`SetBurst()` 动态调整速率
- 用户首次出现在 tracker 中时按匹配到的规则创建 limiter（lazy init）

## Implementation Units

- [ ] **Unit 1: Option 定义与常量注册**

**Goal:** 定义 speed-limiter 服务的配置结构体和类型常量

**Requirements:** R2, R3, R4, R5, R6

**Dependencies:** None

**Files:**
- Modify: `constant/proxy.go` — 添加 `TypeSpeedLimiter = "speed-limiter"`
- Create: `option/speed_limiter.go` — 定义配置结构体

**Approach:**
- 配置结构设计:
  - `SpeedLimiterServiceOptions` — 顶层，包含 `groups`、`users`、`schedules`、`default`
  - `SpeedLimiterGroup` — `name`、`upload_mbps`、`download_mbps`
  - `SpeedLimiterUser` — `name`（用户名）、`group`（可选引用分组）、`upload_mbps`（可选覆盖）、`download_mbps`（可选覆盖）
  - `SpeedLimiterSchedule` — `time_range`（如 "18:00-23:00"）、`upload_mbps`、`download_mbps`、`groups`（可选，只对某些分组生效）
  - `SpeedLimiterDefault` — `upload_mbps`、`download_mbps`（全局默认）
- Mbps 字段使用 `int` 类型，0 表示不限速

**Patterns to follow:**
- `option/user_provider.go` — 服务选项定义风格
- `constant/proxy.go` — 类型常量定义

**Test expectation:** none — 纯配置定义，无行为

**Verification:**
- 编译通过，配置结构体可被 JSON 序列化/反序列化

---

- [ ] **Unit 2: 令牌桶限速连接包装器**

**Goal:** 实现 `ThrottledConn` 和 `ThrottledPacketConn`，在 Read/Write 层面通过令牌桶实现限速

**Requirements:** R1, R2, R8

**Dependencies:** Unit 1（需要 `MbpsToBps` 常量）

**Files:**
- Create: `service/speedlimiter/conn.go` — ThrottledConn, ThrottledPacketConn
- Test: `service/speedlimiter/conn_test.go`

**Approach:**
- `ThrottledConn` 包装 `net.Conn`，持有 upload limiter（控制 Write）、download limiter（控制 Read）和 `context.Context`
- **Context 来源**: 构造时从 `RoutedConnection(ctx, conn, ...)` 的 ctx 传入并存储在 ThrottledConn 中。该 ctx 的生命周期与连接一致，连接关闭时 ctx 被取消，WaitN 随之返回错误
- `Read(p)`: 先调用 inner `Read`，获得 `n` 字节后调用 `waitN(downloadLimiter, n)` 阻塞等待令牌。采用 post-read throttling 设计——因为 Read 前无法预知将读到多少字节，通过延迟返回数据给调用者，TCP 流控最终会对发送方产生背压，实现有效限速
- `Write(p)`: 先调用 `waitN(uploadLimiter, n)` 等待令牌，再调用 inner `Write`
- **分片调用**: `rate.Limiter.WaitN` 当 n > burst 时直接返回错误。因此封装 `waitN(limiter, n)` 辅助函数：当 n > burst 时循环分片调用，每次 WaitN 的 n 不超过 burst 值。这确保大 buffer 的 Read/Write（如 TCP 一次读取 64KB+）不会因超过 burst 而失败
- 如果某方向 limiter 为 nil，则该方向不限速
- `ThrottledPacketConn` 类似，包装 `N.PacketConn`
- 使用 `golang.org/x/time/rate` 的 `rate.NewLimiter(rate.Limit(bps), burst)`

**Patterns to follow:**
- `experimental/v2rayapi/stats.go` — conn 包装的注册模式
- sing 库的 `bufio.NewInt64CounterConn` — conn 包装的接口实现模式

**Test scenarios:**
- Happy path: 创建限速 conn（10Mbps），写入数据，验证实际吞吐不超过配置速率（允许 burst 窗口内的突发）
- Happy path: 只配置 upload limiter 时，download 方向不受限
- Happy path: 只配置 download limiter 时，upload 方向不受限
- Happy path: 多个 ThrottledConn 共享同一 limiter 实例 → 总吞吐被限制在配置速率（R1 核心验证）
- Edge case: limiter 为 nil 时，Read/Write 直接透传不阻塞
- Edge case: 写入 0 字节时不消耗令牌
- Edge case: Read/Write 的 buffer 大于 burst 时，应分片多次 WaitN 而非报错
- Edge case: ThrottledPacketConn 的 ReadFrom/WriteTo 正确消耗令牌
- Error path: context 取消后，WaitN 立即返回错误，conn 操作应传播该错误
- Error path: inner conn 关闭后，Read/Write 返回 inner 的错误而非 limiter 错误

**Verification:**
- 单元测试通过，限速精度在合理范围内（±10%）

---

- [ ] **Unit 3: 用户限速管理器（核心调度逻辑）**

**Goal:** 实现限速规则的解析、用户-limiter 映射、时间段切换逻辑

**Requirements:** R3, R4, R5, R8

**Dependencies:** Unit 1, Unit 2

**Files:**
- Create: `service/speedlimiter/manager.go` — LimiterManager 核心逻辑
- Test: `service/speedlimiter/manager_test.go`

**Approach:**
- `LimiterManager` 持有:
  - `groups map[string]*GroupConfig` — 分组名到配置的映射
  - `userOverrides map[string]*UserConfig` — 用户名到覆盖配置的映射
  - `userGroups map[string]string` — 用户名到分组名的映射
  - `limiters sync.Map` 或 `map[string]*UserLimiter` + `sync.RWMutex` — 用户名到活跃 limiter 对的映射
  - `schedules []ScheduleConfig` — 时间段规则列表
  - `defaultConfig *DefaultConfig` — 全局默认限速
- `UserLimiter` 结构: `upload *rate.Limiter` + `download *rate.Limiter`
- `GetOrCreateLimiter(userName string) *UserLimiter` — 根据优先级查找限速配置:
  1. 用户覆盖（per-user override）
  2. 用户所属分组的配置
  3. 全局默认
  4. 无匹配 → 返回 nil（不限速）
- 时间段调度: 启动一个 goroutine，每分钟检查当前时间是否进入/离开某个 schedule 范围，如果切换则遍历所有活跃 limiter 调用 `SetLimit()`/`SetBurst()`
- **时区**: time_range 使用服务器本地时间（`time.Now().Local()`），不引入额外时区配置
- **Schedule 与 per-user override 优先级**: per-user override 始终优先于 schedule。具体规则:
  1. 用户有 per-user override 的方向（如只覆盖了 upload_mbps）→ 该方向不受 schedule 影响
  2. 用户有 group 且 schedule 指定了该 group → schedule 覆盖 group 的速率（仅限未被 per-user 覆盖的方向）
  3. schedule 的 groups 为空 → 对所有无 per-user override 的用户生效
  4. schedule 结束后恢复到原始速率（group/user/default）

**Patterns to follow:**
- `service/userprovider/service.go` — 服务结构和生命周期管理

**Test scenarios:**
- Happy path: 用户在 userOverrides 中 → 返回用户级 limiter，速率与配置一致
- Happy path: 用户在 userGroups 中且 group 有配置 → 返回分组级 limiter
- Happy path: 用户不在任何规则中但有 default → 返回默认 limiter
- Happy path: 用户不在任何规则中且无 default → 返回 nil
- Happy path: 同一用户多次调用 GetOrCreateLimiter 返回相同实例（指针相等）
- Happy path: 用户同时有 group 和 per-user upload_mbps 覆盖 → upload 用覆盖值，download 用 group 值
- Edge case: 时间段切换后，现有 limiter 的 rate 被更新为 schedule 速率
- Edge case: 时间段结束后，limiter 恢复到原始速率（group/user/default）
- Edge case: time_range 跨午夜（如 "23:00-06:00"）正确匹配
- Edge case: 多个 schedule 时间段重叠时，后定义的 schedule 优先
- Edge case: upload_mbps=0 表示不限速该方向，对应 limiter 为 nil
- Edge case: schedule 的 groups 为空时，对所有用户生效
- Integration: schedule 只指定某些 groups 时，不在该 groups 的用户不受 schedule 影响
- Integration: 并发调用 GetOrCreateLimiter（100 goroutine 同一用户名）→ 全部返回同一实例，无 race

**Verification:**
- 单元测试覆盖所有优先级查找路径和时间段切换逻辑

---

- [ ] **Unit 4: Speed Limiter 服务注册与生命周期**

**Goal:** 实现 speed-limiter 服务的 `ConnectionTracker` 接口和服务注册，集成到 sing-box 启动流程

**Requirements:** R6, R7

**Dependencies:** Unit 1, Unit 2, Unit 3

**Files:**
- Create: `service/speedlimiter/service.go` — Service 实现
- Modify: `include/registry.go` — 注册 speed-limiter 服务
- Modify: `box.go` — 创建服务实例并 `AppendTracker`

**Approach:**
- `Service` 实现 `adapter.ConnectionTracker` + `adapter.Service`（通过嵌入 `boxService.Adapter`）
- `RoutedConnection(ctx, conn, metadata, rule, outbound)`:
  1. 读取 `metadata.User`
  2. 调用 `manager.GetOrCreateLimiter(user)` 获取 limiter 对
  3. 如果 limiter 为 nil → 返回原始 conn
  4. 返回 `NewThrottledConn(conn, ulLimiter, dlLimiter)`
- `RoutedPacketConnection` 同理
- 服务注册: 在 `include/registry.go` 的 `ServiceRegistry()` 中添加 `speedlimiter.RegisterService(registry)`
- **AppendTracker 注册方案**: 在 speed-limiter 的 `Start(StartStateStart)` 中通过 `service.FromContext[adapter.Router](ctx)` 获取 router，调用 `router.AppendTracker(s)` 自行注册。这种方式不需要修改 `box.go` 的服务创建循环，与现有 user-defined service 模式一致。clashServer/v2rayServer 在 `box.go` 中硬编码注册是因为它们是内部服务，speed-limiter 作为用户配置服务应在自身 Start 中完成注册

**Patterns to follow:**
- `service/userprovider/service.go` — `RegisterService` + `NewService` + `Start`/`Close` 模式
- `experimental/v2rayapi/stats.go` — `ConnectionTracker` 实现
- `box.go:351` — `AppendTracker` 注册

**Test scenarios:**
- Happy path: 服务启动后，限速规则中的用户连接被限速
- Happy path: 不在规则中的用户连接不被影响（原始 conn 透传）
- Happy path: metadata.User 为空时（未认证连接 / 无用户协议）→ 返回原始 conn，不限速
- Happy path: RoutedPacketConnection 对 UDP 连接同样应用限速
- Integration: 服务作为 ConnectionTracker 注册后，router 在每个连接上调用 RoutedConnection
- Integration: 服务 Close() 后，已有的 ThrottledConn 仍可正常工作直到连接自然关闭
- Error path: 服务配置为空时优雅处理（无 groups、无 users → 所有连接不限速）

**Verification:**
- `go build` 编译通过
- 服务可正常启动和关闭
- 在 router 的 tracker 链中正确执行

---

- [ ] **Unit 5: 集成测试**

**Goal:** 验证 speed-limiter 服务在完整 sing-box 实例中与各协议的配合

**Requirements:** R1, R2, R7

**Dependencies:** Unit 4

**Files:**
- Create: `test/speed_limiter_test.go`

**Approach:**
- 使用 sing-box 的自测模式：Mixed inbound + 协议 inbound/outbound + speed-limiter 服务
- 配置一个用户限速规则（如 download 1Mbps），通过 SOCKS5 代理传输大量数据，验证吞吐不超过限速值
- 测试无限速用户的吞吐不受影响

**Patterns to follow:**
- `test/box_test.go` — `startInstance`, `testSuit` 等测试辅助函数
- 现有协议测试（如 `test/vmess_test.go`）的自测模式

**Test scenarios:**
- Happy path: 配置用户限速后，该用户的 TCP 传输速率被限制在配置值附近（±15%）
- Happy path: 未配置限速的用户传输速率不受影响
- Happy path: 同一用户建立 3 个并发 TCP 连接 → 总吞吐被限制在配置速率（聚合限速 R1 端到端验证）
- Happy path: 两个不同用户各自限速不同 → 各自独立限速互不干扰
- Integration: speed-limiter 与 user-provider 同时配置时，用户认证和限速都正常工作
- Integration: speed-limiter 与 v2ray stats 共存时，stats 仍然正确统计流量字节数

**Verification:**
- 集成测试通过
- 限速用户的吞吐在预期范围内

## System-Wide Impact

- **Interaction graph:** speed-limiter 通过 `ConnectionTracker` 接口注入 router，对所有协议透明。不修改任何协议的 inbound/outbound 代码。与 v2ray stats 和 clash API 的 tracker 共存于同一链上。
- **Error propagation:** `ThrottledConn` 中 `WaitN` 的 context 错误应传播为 conn 的 Read/Write 错误，触发正常的连接关闭流程。
- **State lifecycle risks:** 用户连接关闭后，对应的 limiter 实例仍保留在 manager 中（因为可能还有其他连接）。需要考虑用户长期不活跃时 limiter 的清理策略（可 defer 到后续版本）。
- **API surface parity:** 无 API 变更。限速是纯配置驱动的服务端行为。
- **Unchanged invariants:** 所有现有协议的认证、路由、规则匹配行为不变。user-provider 的用户加载/推送机制不变。v2ray stats 的流量统计不变。

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| `rate.Limiter.WaitN` 对大 buffer 的 Read/Write 可能导致长时间阻塞 | 考虑分片：单次 WaitN 的 n 上限为 burst 大小，大于 burst 时分多次 WaitN |
| 大量用户各自持有 limiter 实例的内存开销 | `rate.Limiter` 很轻量（几个 int64），1000 用户 × 2 limiter ≈ 几十 KB，可忽略 |
| 时间段切换时的 race condition | `SetLimit`/`SetBurst` 是 goroutine-safe 的（`rate.Limiter` 内部有 mutex） |
| `golang.org/x/time/rate` 从间接依赖变为直接依赖 | 风险极低，这是 Go 官方扩展库 |

## Configuration Example

```json
{
  "services": [
    {
      "type": "speed-limiter",
      "tag": "speed-limiter",
      "default": {
        "upload_mbps": 50,
        "download_mbps": 100
      },
      "groups": [
        {
          "name": "premium",
          "upload_mbps": 100,
          "download_mbps": 200
        },
        {
          "name": "basic",
          "upload_mbps": 10,
          "download_mbps": 20
        }
      ],
      "users": [
        {
          "name": "alice",
          "group": "premium"
        },
        {
          "name": "bob",
          "group": "basic"
        },
        {
          "name": "charlie",
          "upload_mbps": 5,
          "download_mbps": 10
        }
      ],
      "schedules": [
        {
          "time_range": "18:00-23:00",
          "upload_mbps": 50,
          "download_mbps": 100,
          "groups": ["premium"]
        },
        {
          "time_range": "00:00-08:00",
          "upload_mbps": 200,
          "download_mbps": 500
        }
      ]
    }
  ]
}
```

## Sources & References

- `adapter/router.go:33-36` — ConnectionTracker 接口
- `experimental/v2rayapi/stats.go` — ConnectionTracker 实现参考
- `service/userprovider/service.go` — 服务注册和生命周期模式
- `box.go:345-364` — AppendTracker 注册模式
- `constant/speed.go` — MbpsToBps 常量
- `golang.org/x/time/rate` — 令牌桶实现
