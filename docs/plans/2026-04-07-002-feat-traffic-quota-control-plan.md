---
title: "feat: Per-User Traffic Quota Control"
type: feat
status: active
date: 2026-04-07
---

# feat: Per-User Traffic Quota Control

## Overview

在现有 speed-limiter（限速）基础上，新增独立的 `traffic-quota` 服务，实现按用户/用户组的时间段总流量配额控制。当用户的累计流量（上行+下行）超过配额时，断开所有现有连接并阻止新连接，直到配额重置。流量统计状态通过 Redis/Postgres 持久化，重启后不丢失。

## Problem Frame

限速（speed-limiter）控制的是"每秒能用多少带宽"，而流量配额控制的是"在一个周期内能用多少总流量"。运营场景中需要对用户按月/日/自定义周期设置流量上限（如每月 100GB），超限后断开服务。sing-box 已有 `ConnectionTracker` 接口和 `bufio.NewInt64CounterConn` 的流量计数模式，可以零协议修改地实现此功能。

## Requirements Trace

- R1. 统计每个用户的上行+下行总流量（字节数），跨所有连接聚合
- R2. 支持按用户和用户组设置流量配额（单位 GB/MB）
- R3. 支持预定义周期（daily/weekly/monthly）和自定义周期（起始时间+天数）
- R4. 用户累计流量超过配额时，断开所有现有连接并阻止新连接
- R5. 配额在周期结束时自动重置
- R6. 流量统计状态通过 Redis/Postgres 持久化，进程重启后恢复
- R7. 不修改任何协议层代码，通过 `ConnectionTracker` 接口透明生效
- R8. 作为独立服务注册，与 speed-limiter、user-provider 解耦

## Scope Boundaries

- **不做**: 实时流量查询 API（可后续扩展，基础数据结构支持）
- **不做**: 流量预警/通知（如用到 80% 时提醒）
- **不做**: 按方向（上行/下行）分别设置配额（本期只做上行+下行总量）
- **不做**: 文件/HTTP 持久化（只支持 Redis 和 Postgres，与现有数据源复用）
- **支持**: 无 Redis/Postgres 时降级为内存模式（重启后重置），这是有意支持的部署路径，不只是错误处理
- **不做**: 与 v2ray stats 的计数器集成（独立计数，避免耦合）

## Context & Research

### Relevant Code and Patterns

- `adapter/router.go:33-36` — `ConnectionTracker` 接口，流量配额服务的注入点
- `service/speedlimiter/service.go` — 相同模式：`ConnectionTracker` + `service.FromContext[adapter.Router]` 自注册
- `service/speedlimiter/conn.go` — 连接包装模式参考，**不实现 Upstream()** 防止 bufio 绕过
- `experimental/v2rayapi/stats.go` — `bufio.NewInt64CounterConn` 的使用模式，per-user 原子计数器
- `service/userprovider/source_redis.go` — Redis 连接和操作模式
- `service/userprovider/source_postgres.go` — Postgres 连接和操作模式
- `route/route.go:156-158` — tracker 包装位置，conn 关闭传播路径
- `option/speed_limiter.go` — 服务选项定义风格（groups/users/schedules）

### Key Insight: 连接强制关闭机制

`RoutedConnection` 返回的包装 conn 被 outbound 持有。在包装 conn 的 `Read`/`Write` 中返回错误即可触发连接关闭——`route/conn.go` 的 copy 循环会在 I/O 错误后关闭两端并调用 `onClose`。方案：

1. 包装 conn（QuotaConn）持有一个 `closed atomic.Bool` 标志
2. 配额管理器在超限时设置该用户所有 QuotaConn 的 `closed` 标志
3. QuotaConn 的 `Read`/`Write` 在每次调用前检查 `closed`，若为 true 则返回 `ErrQuotaExceeded`
4. 新连接在 `RoutedConnection` 中直接返回已关闭的 conn（`closed=true`），立即触发断连

## Key Technical Decisions

- **独立计数 vs 复用 v2ray stats**: 独立计数。v2ray stats 是可选模块，其计数器仅在内存中且无持久化。独立计数保证配额服务的自洽性。
- **持久化策略**: 周期性（如每 30 秒）将内存中的增量流量写入 Redis/Postgres。每次 flush 后从 DB 读回总量更新内存计数器，确保多实例部署时内存值与 DB 同步（其他实例的增量会通过 DB 反映到本实例）。不做每次 Read/Write 的实时写入（太贵）。
- **配额检查频率**: 在每次 `Read`/`Write` 的计数回调中检查是否超限（O(1) 原子操作比较），不依赖定时器。采用 **post-check 设计**——数据传输完成后才检查，这意味着最后一次 I/O 可能导致实际用量略超配额（最多一个 buffer 大小，通常 64KB）。对于 GB 级配额可忽略；集成测试中使用小配额时需注意这个特性。
- **连接跟踪**: QuotaManager 使用 `compatible.Map[string, *connList]`（项目自有的泛型 `sync.Map` 包装器）管理用户到活跃连接列表的映射。选择 `compatible.Map` 而非 `sync.RWMutex + map`，原因：连接注册/移除是高频操作（每个新连接都触发），`sync.Map` 的无锁读和 per-key 独立写避免全局锁竞争；项目中 Clash API 的连接跟踪（`experimental/clashapi/trafficontrol/manager.go:42`）已使用完全相同的模式。每个用户的 `connList` 内部使用 `sync.Mutex` 保护 slice 操作（注册/移除/遍历），锁粒度为单用户级别，不同用户之间完全无竞争。
- **周期重置**: 使用定时器周期检查是否进入新周期。重置时清零 DB 中的计数并重置内存计数器。
- **与 speed-limiter 的共存**: 两个服务都是 `ConnectionTracker`，conn 会被依次包装。顺序无关——quota 的计数和 speed-limiter 的限速互不干扰。

## Open Questions

### Resolved During Planning

- **Q: 如何强制断开现有连接？** A: QuotaConn 持有 `closed` 标志，超限时设置标志，下一次 Read/Write 返回错误触发连接关闭。
- **Q: 配额单位？** A: 配置中使用 `quota_gb` (float64)，内存中统一转为字节。支持小数（如 0.5GB = 512MB）。
- **Q: 持久化数据的 key 格式？** A: Redis: `traffic-quota:{user}:{period_key}` → 字节数。Postgres: 表 `traffic_quota`，列 `user_name, period_key, bytes_used, updated_at`。
- **Q: 多实例部署时的一致性？** A: Redis/Postgres 是共享存储，多实例的增量写入会累加。启动时从 DB 读取当前周期的已用流量。

### Deferred to Implementation

- Redis/Postgres 的精确 schema 和 SQL 语句
- 持久化写入的批量优化策略
- 周期重置时的时区处理细节

## High-Level Technical Design

> *This illustrates the intended approach and is directional guidance for review, not implementation specification. The implementing agent should treat it as context, not code to reproduce.*

```
┌─────────────────────────────────────────────────┐
│              traffic-quota config                │
│  groups:                                         │
│    - name: basic, quota_gb: 100, period: monthly │
│  users:                                          │
│    - name: alice, group: basic                   │
│    - name: bob, quota_gb: 10, period: daily      │
│  persistence:                                    │
│    redis: {address: ..., key_prefix: "tq:"}      │
└──────────────────────┬──────────────────────────┘
                       │
                       ▼
┌──────────────────────────────────────────────────┐
│              QuotaManager                        │
│  ┌─────────────┐   ┌──────────────────────┐      │
│  │ userQuotas   │   │ userUsage            │      │
│  │ alice→100GB  │   │ alice→45.2GB (mem)   │      │
│  │ bob  →10GB   │   │ bob  → 9.8GB (mem)   │      │
│  └─────────────┘   └──────────────────────┘      │
│  ┌─────────────┐   ┌──────────────────────┐      │
│  │ activeConns  │   │ exceeded             │      │
│  │ alice→[c1,c2]│   │ bob → true           │      │
│  │ bob  →[c3]   │   │                      │      │
│  └─────────────┘   └──────────────────────┘      │
│                                                   │
│  Periodic: flush deltas → Redis/Postgres          │
│  Periodic: check period reset                     │
└──────────────────────┬───────────────────────────┘
                       │
    RoutedConnection   │
         ┌─────────────┴─────────────┐
         ▼                           ▼
  User exceeded?              User OK?
  → return dead conn          → return QuotaConn
    (Read/Write → error)        (counts bytes,
                                 checks quota on
                                 each I/O)
```

**QuotaConn 数据流:**
```
QuotaConn.Read(p):
  1. if closed.Load() → return ErrQuotaExceeded
  2. n, err = inner.Read(p)
  3. manager.AddBytes(user, n)  // atomic add + check
  4. return n, err

QuotaConn.Write(p):
  1. if closed.Load() → return ErrQuotaExceeded
  2. n, err = inner.Write(p)
  3. manager.AddBytes(user, n)  // atomic add + check
  4. return n, err

manager.AddBytes(user, n):
  1. atomic.AddInt64(&usage, n)
  2. if usage > quota:
     → set exceeded[user] = true
     → for each conn in activeConns[user]:
          conn.closed.Store(true)
```

## Implementation Units

- [ ] **Unit 1: Option 定义与常量注册**

**Goal:** 定义 traffic-quota 服务的配置结构体和类型常量

**Requirements:** R2, R3, R8

**Dependencies:** None

**Files:**
- Modify: `constant/proxy.go` — 添加 `TypeTrafficQuota = "traffic-quota"`
- Create: `option/traffic_quota.go` — 定义配置结构体

**Approach:**
- `TrafficQuotaServiceOptions`: 顶层，包含 `groups`、`users`、`persistence`、`flush_interval`（使用 `badoption.Duration` 类型，与 user-provider 的 `update_interval` 一致）
- `TrafficQuotaGroup`: `name`, `quota_gb` (float64), `period` (string: "daily"/"weekly"/"monthly"), `period_start` (可选自定义起始), `period_days` (可选自定义天数)
- `TrafficQuotaUser`: `name`, `group` (可选), `quota_gb` (可选覆盖), `period` (可选覆盖)
- `TrafficQuotaPersistence`: `redis` (*TrafficQuotaRedisOptions), `postgres` (*TrafficQuotaPostgresOptions)
- Redis/Postgres 选项复用 user-provider 的结构模式（address, password, db, connection_string 等）

**Patterns to follow:**
- `option/speed_limiter.go` — 配置结构风格
- `option/user_provider.go` — Redis/Postgres 选项

**Test expectation:** none — 纯配置定义

**Verification:**
- 编译通过，JSON 序列化/反序列化正确

---

- [ ] **Unit 2: QuotaConn 连接包装器**

**Goal:** 实现 QuotaConn，在每次 Read/Write 时统计字节数，并在超限时返回错误

**Requirements:** R1, R4, R7

**Dependencies:** Unit 1

**Files:**
- Create: `service/trafficquota/conn.go`
- Test: `service/trafficquota/conn_test.go`

**Approach:**
- `QuotaConn` 包装 `net.Conn`，持有:
  - `closed atomic.Bool` — 超限标志
  - `onBytes func(n int)` — 字节计数回调（由 manager 注入）
  - `onClose func()` — 连接关闭时从活跃列表移除
- Read/Write: 先检查 `closed`，再执行 I/O，最后调用 `onBytes(n)`
- **不实现 Upstream()** — 防止 bufio 绕过
- `QuotaPacketConn` 同理包装 `N.PacketConn`
- `ErrQuotaExceeded` 定义为 sentinel error
- 实现 `Close()` 时调用 `onClose()` 清理

**Patterns to follow:**
- `service/speedlimiter/conn.go` — 包装模式，不实现 Upstream()

**Test scenarios:**
- Happy path: Read/Write 正常透传，onBytes 回调被调用且 n 值正确
- Happy path: closed=false 时 Read/Write 正常工作
- Edge case: closed=true 时 Read 返回 ErrQuotaExceeded
- Edge case: closed=true 时 Write 返回 ErrQuotaExceeded
- Edge case: 运行中设置 closed=true，下一次 Read/Write 立即返回错误
- Error path: inner conn 关闭后 Read/Write 返回 inner 的错误
- Integration: Close() 触发 onClose 回调

**Verification:**
- 单元测试通过

---

- [ ] **Unit 3: QuotaManager 配额管理核心**

**Goal:** 实现配额规则解析、用户流量跟踪、超限判定、活跃连接管理

**Requirements:** R1, R2, R3, R4, R5

**Dependencies:** Unit 2

**Files:**
- Create: `service/trafficquota/manager.go`
- Test: `service/trafficquota/manager_test.go`

**Approach:**
- `QuotaManager` 持有:
  - `groups map[string]*QuotaConfig` — 分组配额配置
  - `userGroups map[string]string` — 用户到分组映射
  - `userOverrides map[string]*QuotaConfig` — 用户级配额覆盖
  - `usage map[string]*atomic.Int64` — 用户名到已用字节数（内存中的快速路径）
  - `exceeded sync.Map` 或 `compatible.Map[string, bool]` — 超限状态缓存
  - `activeConns compatible.Map[string, *connList]` — 用户的活跃连接列表（connList 内部 sync.Mutex 保护 slice）
  - `periods map[string]*PeriodConfig` — 用户到周期配置
- `QuotaConfig`: `quotaBytes int64`, `period PeriodType`, `periodStart time.Time`, `periodDays int`
- `PeriodType`: `daily`, `weekly`, `monthly`, `custom`
- `RegisterConn(user, conn)` — 添加到活跃列表，返回 `onBytes`/`onClose` 回调
- `AddBytes(user, n)` — 原子累加，检查超限，超限时批量关闭
- `IsExceeded(user) bool` — 用于 RoutedConnection 判断是否阻止新连接
- `CheckPeriodReset(now)` — 检查各用户是否进入新周期，重置计数
- `GetCurrentPeriodKey(user, now) string` — 计算当前周期标识符（如 "2026-04" for monthly）
- `LoadUsage(user, bytes)` — 从持久化恢复已用流量

**Patterns to follow:**
- `service/speedlimiter/manager.go` — 分组/用户/优先级解析

**Test scenarios:**
- Happy path: 用户流量未超限 → IsExceeded=false
- Happy path: 用户流量超限 → IsExceeded=true，所有活跃连接 closed=true
- Happy path: 用户组配额 → 组内用户继承配额
- Happy path: 用户覆盖配额 → 覆盖优先于组
- Edge case: 无配额的用户 → 不跟踪，不限制
- Edge case: 并发 AddBytes（100 goroutine）→ 计数正确，超限只触发一次
- Edge case: 连接关闭后从活跃列表移除，不影响其他连接
- Integration: 周期重置 → usage 归零，exceeded 清除，已断开的连接不恢复
- Happy path: daily 周期 — 跨天重置
- Happy path: monthly 周期 — 跨月重置
- Edge case: custom 周期 — 按 period_start + period_days 计算
- Edge case: LoadUsage 后流量继续累加，不重复计数
- Integration: 并发注册/移除连接（50 goroutine 同一用户）+ 同时 AddBytes → 无 race，计数正确（验证 compatible.Map + connList 锁安全）
- Integration: 超限遍历关闭连接期间，新连接注册不死锁（per-user 锁粒度验证）
- Edge case: post-check 溢出行为 — 配额 500 字节，单次 Write 1024 字节 → 实际用量 1024，超限触发（允许溢出但不丢失）
- Integration: 多实例模拟 — LoadUsage 加载 DB 值后 AddBytes 继续累加到超限（模拟 flush 同步场景）

**Verification:**
- 单元测试覆盖所有配额判定路径和周期重置逻辑
- 并发测试通过 `-race` 检测无竞态

---

- [ ] **Unit 4: 持久化层（Redis + Postgres）**

**Goal:** 实现流量统计的持久化存储和恢复

**Requirements:** R6

**Dependencies:** Unit 3

**Files:**
- Create: `service/trafficquota/persist_redis.go` (build tag `with_redis`)
- Create: `service/trafficquota/persist_redis_stub.go` (build tag `!with_redis`)
- Create: `service/trafficquota/persist_postgres.go` (build tag `with_postgres`)
- Create: `service/trafficquota/persist_postgres_stub.go` (build tag `!with_postgres`)
- Create: `service/trafficquota/persist.go` — Persister 接口定义
- Test: `service/trafficquota/persist_test.go`

**Approach:**
- `Persister` 接口:
  - `Load(user, periodKey) (int64, error)` — 加载单个用户已用字节数
  - `LoadAll(periodKey) (map[string]int64, error)` — 批量加载所有用户已用字节数（启动恢复和 flush 后同步用）
  - `Save(user, periodKey, bytes int64) error` — 保存已用字节数（覆盖写）
  - `IncrBy(user, periodKey, delta int64) error` — 增量写入（原子操作）
  - `Delete(user, periodKey) error` — 周期重置时清除旧数据
  - `Close() error`
- Redis 实现: key = `{prefix}:{user}:{periodKey}`，使用 `INCRBY` 增量写入，`GET` 读取
- Postgres 实现: 表 `traffic_quota(user_name, period_key, bytes_used, updated_at)`，使用 `INSERT ON CONFLICT UPDATE SET bytes_used = bytes_used + $delta`
- Flush 机制: manager 维护 `pendingDeltas map[string]int64`（内存增量），定时 flush 调用 `IncrBy` 写入 DB，然后清零 pendingDeltas
- Build tag 模式与 `service/userprovider/source_redis.go` 一致

**Patterns to follow:**
- `service/userprovider/source_redis.go` — Redis 客户端初始化
- `service/userprovider/source_postgres.go` — Postgres 连接池

**Test scenarios:**
- Happy path: Save 后 Load 返回正确字节数
- Happy path: IncrBy 多次后 Load 返回累加值
- Happy path: Delete 后 Load 返回 0
- Happy path: LoadAll 返回所有用户的已用字节数
- Happy path: LoadAll 在无数据时返回空 map
- Edge case: Load 不存在的 key → 返回 0, nil
- Edge case: IncrBy 后 LoadAll 包含增量后的值（验证 flush-sync 读回场景）
- Error path: 连接失败时返回错误

**Verification:**
- 单元测试通过（使用 mock 或本地 Redis/Postgres）

---

- [ ] **Unit 5: TrafficQuota 服务注册与生命周期**

**Goal:** 实现 traffic-quota 服务，整合 QuotaManager + Persister + ConnectionTracker

**Requirements:** R7, R8

**Dependencies:** Unit 1, 2, 3, 4

**Files:**
- Create: `service/trafficquota/service.go`
- Modify: `include/registry.go` — 注册服务

**Approach:**
- `Service` 实现 `adapter.ConnectionTracker` + `adapter.Service`
- `Start(StartStateStart)`:
  1. `service.FromContext[adapter.Router](ctx).AppendTracker(s)` 注册
  2. 初始化 Persister（Redis/Postgres）
  3. 加载所有已配置用户的当前周期流量（从 DB 恢复）
  4. 启动 flush 定时器（默认 30 秒）
  5. 启动周期重置检查定时器（每分钟）
- `RoutedConnection`:
  1. `metadata.User` 为空 → 返回原始 conn
  2. `manager.IsExceeded(user)` → 返回已 closed 的 QuotaConn（立即断开）
  3. 否则 → 注册连接，返回 QuotaConn
- `RoutedPacketConnection` 同理
- `Close()`: flush 所有 pending deltas（失败时 log error 并将错误包含在返回值中，让 `common.Close` 聚合报告），然后关闭 Persister

**Patterns to follow:**
- `service/speedlimiter/service.go` — 完整的服务注册和 ConnectionTracker 模式

**Test scenarios:**
- Happy path: 服务启动后，有配额的用户连接被跟踪
- Happy path: 无配额用户的连接不受影响
- Happy path: metadata.User 为空时透传
- Integration: 用户流量超限后，新连接立即被断开
- Integration: 用户流量超限后，现有连接的下一次 I/O 返回错误
- Error path: Persister 不可用时服务仍可启动（降级为内存模式并 warn）
- Error path: Close() 时 Persister flush 失败 → 返回错误（不静默丢失）
- Integration: 周期重置后，之前被限制的用户可以正常使用
- Integration: 无 persistence 配置时以内存模式启动，功能正常（重启后计数归零）
- Integration: flush 周期触发后，内存计数器与 DB 值同步（模拟另一实例的增量通过 DB 反映）

**Verification:**
- 编译通过
- 服务可正常启动/关闭
- 与 speed-limiter 在同一 tracker 链上共存

---

- [ ] **Unit 6: 集成测试**

**Goal:** 验证 traffic-quota 在完整 sing-box 实例中的端到端行为

**Requirements:** R1, R4, R7

**Dependencies:** Unit 5

**Files:**
- Create: `test/traffic_quota_test.go`

**Approach:**
- 使用 sing-box 自测模式（Mixed + Trojan + Direct）
- 配置一个用户配额为 500KB（极小值便于快速测试）
- 传输 > 500KB 数据后验证连接被断开
- 验证断开后新连接立即失败

**Patterns to follow:**
- `test/speed_limiter_test.go` — 集成测试模式

**Test scenarios:**
- Happy path: 传输 < 配额的数据 → 连接正常
- Happy path: 传输 > 配额的数据 → 连接被断开（Read/Write 返回错误）
- Happy path: 超限后建立新连接 → 立即断开
- Happy path: 无配额用户传输不受限
- Integration: traffic-quota + speed-limiter 同时配置 → 两者都生效（既限速又限量）
- Edge case: post-check 溢出 — 配额 500KB，下载 600KB → 连接在 500KB~564KB 之间断开（允许最多一个 buffer 的溢出）
- Integration: 无 persistence 的内存模式下配额功能完整（仅重启丢失状态）

**Verification:**
- 集成测试通过
- `-race` 检测无竞态

## System-Wide Impact

- **Interaction graph:** traffic-quota 通过 `ConnectionTracker` 接口注入 router，与 speed-limiter、v2ray stats、clash API 共存于 tracker 链上。不修改任何协议代码。
- **Error propagation:** QuotaConn 返回 `ErrQuotaExceeded`，通过 copy 循环传播到连接关闭。
- **State lifecycle risks:**
  - 进程 crash 时内存中的未 flush 增量会丢失（最多 flush_interval 的数据量）
  - 多实例部署时依赖 Redis/Postgres 的原子操作保证一致性
- **API surface parity:** 无 API 变更。纯配置驱动。
- **Unchanged invariants:** 所有协议的认证/路由/规则匹配不变。speed-limiter 的限速不变。user-provider 的用户管理不变。

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| Redis/Postgres 不可用导致服务启动失败 | 降级为内存模式，log warn，不阻塞启动 |
| flush 间隔内 crash 丢失增量 | 可配置 flush_interval 缩短间隔；增量丢失影响有限 |
| 大量用户导致 activeConns map 内存增长 | 连接关闭时及时移除；map 本身轻量 |
| 超限时批量关闭连接的并发安全 | 使用 `closed.Store(true)` 原子操作，无锁竞争 |
| 多 sing-box 实例共享 DB 的竞态 | Redis `INCRBY` / Postgres `UPDATE ... + $delta` 都是原子操作 |

## Configuration Example

```json
{
  "services": [
    {
      "type": "traffic-quota",
      "tag": "quota",
      "groups": [
        {
          "name": "basic",
          "quota_gb": 100,
          "period": "monthly"
        },
        {
          "name": "daily-plan",
          "quota_gb": 2,
          "period": "daily"
        }
      ],
      "users": [
        {"name": "alice", "group": "basic"},
        {"name": "bob", "group": "daily-plan"},
        {"name": "charlie", "quota_gb": 0.5, "period": "custom", "period_start": "2026-04-01", "period_days": 7}
      ],
      "persistence": {
        "redis": {
          "address": "127.0.0.1:6379",
          "key_prefix": "tq:"
        }
      },
      "flush_interval": "30s"
    }
  ]
}
```

## Sources & References

- `adapter/router.go:33-36` — ConnectionTracker 接口
- `service/speedlimiter/` — 完整的 ConnectionTracker 服务模式
- `experimental/v2rayapi/stats.go` — 字节计数模式
- `service/userprovider/source_redis.go` — Redis 操作模式
- `service/userprovider/source_postgres.go` — Postgres 操作模式
- `route/route.go:156-158` — tracker 链执行位置
