---
title: "feat: Per-client (per source IP) speed limiting mode"
type: feat
status: active
date: 2026-04-09
origin: docs/plans/2026-04-07-001-feat-per-user-speed-limiter-plan.md
---

# feat: Per-client (per source IP) speed limiting mode

## Overview

当前限速器按用户名维度共享 limiter：同一用户所有连接（无论来自几个 sing-box 客户端）共享同一个速率限制。需要新增 `per_client` 模式，使同一用户从不同 sing-box 客户端连入时，每个客户端实例独享一份限速配额。

sing-box 客户端通过代理协议连接服务端时，同一客户端实例的所有连接共享同一个源 IP，因此可以用 `user + sourceIP` 作为 limiter 的复合 key 来区分不同客户端。

## Problem Frame

用户 A 配置 10Mbps，从 3 个不同终端（每个终端运行一个 sing-box 客户端）连入：
- **现状 (per-user)**：3 个终端共享 10Mbps
- **目标 (per-client)**：每个终端独享 10Mbps，总带宽可达 30Mbps；同一终端内多个连接/下载进程仍共享该终端的 10Mbps

## Requirements Trace

- R1. 新增 `per_client` 配置项，支持在 default / group / user 三级配置
- R2. 优先级：user > group > default（与现有速度配置优先级一致）
- R3. 默认值为 `false`，保持现有 per-user 行为不变（向后兼容）
- R4. per_client 模式下，同一用户不同源 IP 各自拥有独立 limiter
- R5. per_client 模式下，schedule 变更仍能正确更新所有该用户的 client limiter
- R6. 动态配置 (ApplyConfig/RemoveConfig) 也支持 per_client 字段
- R7. 热路径（RoutedConnection）完全无锁：不得使用 `sync.Mutex`/`sync.RWMutex`（包括 `RLock`），仅使用 `compatible.Map`（基于 `sync.Map`）、`sync/atomic` 和 `atomic.Pointer` 操作
- R8. 不得影响现有业务逻辑的性能和正确性
- R9. client limiter 需有 TTL 自动清理机制，避免用户换 IP/断线重连后旧 limiter 无限堆积

## Scope Boundaries

- 不改变 per-user 模式的任何行为
- 不实现 per-connection（每个 TCP 连接独立限速）模式
- 不实现 client 数量限制或 client 列表查询 API（可作为后续功能）
- 暂不处理 NAT 场景下多个物理设备共享同一源 IP 的问题（这是部署层面的约束）

## Context & Research

### Relevant Code and Patterns

- `option/speed_limiter.go` — 配置结构定义
- `service/speedlimiter/manager.go` — `LimiterManager`，核心 limiter 管理，`limiters map[string]*UserLimiter` 以用户名为 key
- `service/speedlimiter/service.go` — `RoutedConnection`/`RoutedPacketConnection`，可访问 `metadata.Source.Addr` 获取源 IP
- `service/speedlimiter/conn.go` — `NewLimiter`、`NewThrottledConn` 等
- `adapter/inbound.go:51` — `Source M.Socksaddr` 字段，`.Addr` 返回 `netip.Addr`

### Existing Priority Pattern

速度配置已有三级合并逻辑 (`resolveConfig`)：default → group override → user override。`per_client` 应遵循相同模式。

## Key Technical Decisions

- **用源 IP 标识客户端**：`metadata.Source.Addr` 是 `netip.Addr`，同一 sing-box 客户端的所有连接共享同一源 IP。这是最简单且符合实际场景的方案。
- **复合 key 分隔符用 `|`**：per_client 模式下 limiter map key 为 `"user|sourceIP"`（如 `"alice|1.2.3.4"`、`"alice|2001:db8::1"`）。**不能用 `:`**，因为 IPv6 地址本身包含 `:`，`strings.Cut(key, ":")` 会错误分割。`|` 不会出现在用户名和 IP 地址中。per_user 模式保持 `"user"` 不变。
- **group/user 用 `*bool` 类型**：允许区分"未设置"和"显式设为 false"，实现三级 fallback。default 用普通 `bool`（零值 false 即默认行为）。
- **manager 持有 perClient 配置**：`speedConfig` 新增 `PerClient bool` 字段，`resolveConfig` 合并时处理。
- **service 层传递 sourceIP**：`RoutedConnection` 调用 manager 时传入 source addr，manager 根据模式决定 key。
- **完全无锁热路径**：
  - **配置快照 + `atomic.Pointer`**：将所有配置数据（defaultConfig, groups, userGroups, userOverrides, userRawConfig, schedules, userSchedules）打包到一个不可变的 `configSnapshot` 结构体中，通过 `atomic.Pointer[configSnapshot]` 存储。热路径只需 `config.Load()` 原子读取，零锁。冷路径（ApplyConfig/schedule 更新）构建新 snapshot 后 `config.Store()` 原子替换。
  - **limiter 用 `compatible.Map`**：`limiters` 从 `map[string]*UserLimiter` + `sync.RWMutex` 改为 `compatible.Map[string, *UserLimiter]`（基于 `sync.Map`），热路径上只需 `LoadOrStore`。
  - **`rate.Limiter` 内部并发安全**：多个连接共享同一个 limiter 无需外部加锁。
  - **去掉 `mu sync.RWMutex`**：配置由 `atomic.Pointer` 保护，limiters 由 `compatible.Map` 保护，不再需要任何互斥锁。
  - 唯一保留的锁：`service.go` 中的 `applyMu sync.Mutex`，仅保护动态配置写入的串行化，不在热路径上。

## Open Questions

### Resolved During Planning

- **Q: 如何清理不再活跃的 client limiter？** → `UserLimiter` 增加 `lastActive` 字段（`atomic.Int64` 存储 Unix 时间戳），每次 `GetOrCreateLimiterForClient` 和 `waitN` 时原子更新。复用已有的 schedule loop（每分钟执行），在 `CheckSchedules` 中额外遍历 `limiters`，删除超过 TTL（默认 10 分钟）未活跃的 client limiter（仅 per_client 模式的复合 key）。
- **Q: schedule 更新时如何遍历所有 client limiter？** → `updateLimiterRatesLocked` 已遍历所有 `m.limiters` 中的 entry，复合 key 不影响遍历，只需从 key 中提取 userName 来查找配置。

### Deferred to Implementation

- per_client 模式下的 API 查询接口（如列出某用户的所有活跃 client）

## High-Level Technical Design

> *This illustrates the intended approach and is directional guidance for review, not implementation specification.*

```
配置层:
  default.per_client = true/false
  group[x].per_client = *bool (nil = inherit)
  user[x].per_client = *bool (nil = inherit)

解析: resolveConfig(userName) -> speedConfig { UploadMbps, DownloadMbps, PerClient }

运行时 limiter key:
  PerClient=false → key = "alice"           (现有行为)
  PerClient=true  → key = "alice:1.2.3.4"  (新行为)

调用链:
  service.RoutedConnection(ctx, conn, metadata, ...)
    → sourceAddr = metadata.Source.Addr
    → manager.GetOrCreateLimiterForClient(userName, sourceAddr)
      → snap = config.Load()              // atomic.Pointer，零锁
      → cfg = snap.resolveConfig(userName) // 纯函数，读不可变快照
      → if cfg.PerClient: key = userName + "|" + sourceAddr.String()
      → else:             key = userName
      → limiters.LoadOrStore(key, newLimiter)  // compatible.Map，无锁
      → limiter.Touch()                        // atomic.Store，仅在查找时
    → NewThrottledConn(conn, limiter.Upload, limiter.Download)

并发策略（完全无锁热路径）:
  atomic.Pointer[configSnapshot] → 配置数据不可变快照，原子读
  limiters (compatible.Map)      → 无锁并发访问 limiter 实例
  rate.Limiter                   → 内部自带 mutex，并发安全
  lastActive (atomic.Int64)      → 原子更新活跃时间

  热路径: atomic.Load(配置) → LoadOrStore(查找limiter) → atomic.Store(活跃时间) → 完成
  冷路径: 构建新snapshot → atomic.Store(配置) → Range(更新所有limiter速率) → 完成
```

## Implementation Units

- [ ] **Unit 1: 配置结构扩展**

**Goal:** 在 option 层添加 `per_client` 字段

**Requirements:** R1, R3

**Dependencies:** None

**Files:**
- Modify: `option/speed_limiter.go`

**Approach:**
- `SpeedLimiterDefault` 添加 `PerClient bool`
- `SpeedLimiterGroup` 添加 `PerClient *bool`（指针，允许 nil = 不覆盖）
- `SpeedLimiterUser` 添加 `PerClient *bool`（同上）

**Patterns to follow:**
- 现有 `UploadMbps`/`DownloadMbps` 的 JSON tag 风格

**Test expectation:** none — 纯数据结构变更，由 Unit 3 覆盖

**Verification:**
- JSON 序列化/反序列化正确处理 `per_client` 字段
- 省略时默认 false/nil

---

- [ ] **Unit 2: Manager 支持 per_client 解析与 limiter 管理**

**Goal:** `speedConfig` 新增 `PerClient`，`resolveConfig` 三级合并，limiter map 改用 `compatible.Map` 支持无锁热路径

**Requirements:** R2, R4, R5, R6, R7, R8

**Dependencies:** Unit 1

**Files:**
- Modify: `service/speedlimiter/manager.go`
- Test: `service/speedlimiter/manager_test.go`

**Approach:**
- 新增 `configSnapshot` 不可变结构体，包含所有配置字段（defaultConfig, groups, userGroups, userOverrides, userRawConfig, schedules, userSchedules 以及新增的 PerClient 相关字段）
- `LimiterManager` 用 `atomic.Pointer[configSnapshot]` 替代 `mu sync.RWMutex` + 散落的 map 字段
- `speedConfig` 新增 `PerClient bool`
- `resolveConfig` 改为 `configSnapshot` 的方法（纯函数，读不可变数据），合并逻辑：default 的 PerClient 作为基础，group 的 `*bool` 非 nil 则覆盖，user 的 `*bool` 非 nil 则覆盖
- `limiters` 从 `map[string]*UserLimiter` 改为 `compatible.Map[string, *UserLimiter]`
- **去掉 `mu sync.RWMutex`**，所有锁全部消除：
  - 热路径 `GetOrCreateLimiterForClient`：`config.Load()` 原子读快照 → `snap.resolveConfig(userName)` → 算 key → `limiters.LoadOrStore`
  - 冷路径：构建新 `configSnapshot` → `config.Store(newSnap)` → `limiters.Range` 遍历更新速率
- 新增 `GetOrCreateLimiterForClient(userName string, sourceAddr netip.Addr) *UserLimiter` 方法：
  - `snap := m.config.Load()` 原子读配置快照
  - `cfg := snap.resolveConfig(userName)` 获取含 PerClient 的配置
  - 如果 `cfg.PerClient` 为 true，key = `userName + "|" + sourceAddr.String()`
  - 否则 key = `userName`
  - `limiters.LoadOrStore(key, newLimiter)` — miss 时原子创建
- 保留原 `GetOrCreateLimiter(userName)` 方法不变（向后兼容，内部委托新方法）
- `updateLimiterRates`：通过 `limiters.Range` 遍历，从复合 key 中用 `strings.Cut(key, "|")` 提取 userName 查找配置
- `ApplyConfig`/`RemoveConfig`：构建新 snapshot 原子替换，再通过 `limiters.Range` 遍历删除该 user 的所有 client limiter（key 前缀匹配）

**Test scenarios:**
- Happy path: per_client=true 时，同用户不同 sourceAddr 获得不同 limiter 实例
- Happy path: per_client=false 时，同用户不同 sourceAddr 获得相同 limiter 实例
- Happy path: 三级优先级合并 — user 覆盖 group，group 覆盖 default
- Edge case: user per_client=nil，group per_client=nil，fallback 到 default
- Edge case: user 显式 per_client=false 覆盖 group per_client=true
- Integration: schedule 变更后，所有该用户的 client limiter 速率都更新
- Integration: RemoveConfig 后，该用户所有 client limiter 被清理
- Integration: ApplyConfig 动态变更 per_client 后，新连接使用新模式

**Verification:**
- 所有 manager_test.go 测试通过
- `go build ./...` 编译通过

---

- [ ] **Unit 3: Service 层传递 sourceAddr**

**Goal:** `RoutedConnection`/`RoutedPacketConnection` 提取 source IP 并调用新方法

**Requirements:** R4

**Dependencies:** Unit 2

**Files:**
- Modify: `service/speedlimiter/service.go`

**Approach:**
- `RoutedConnection` 从 `metadata.Source.Addr` 获取 `netip.Addr`
- 调用 `s.manager.GetOrCreateLimiterForClient(user, sourceAddr)` 替代 `GetOrCreateLimiter(user)`
- `RoutedPacketConnection` 同理
- 当 per_client=false 时，manager 内部忽略 sourceAddr，行为不变

**Test scenarios:**
- Happy path: per_client 模式下，不同 source IP 的连接各自限速独立
- Happy path: per_user 模式下，不同 source IP 的连接共享限速（回归验证）

**Verification:**
- `go build -tags "..." ./...` 编译通过
- 现有测试不受影响

---

- [ ] **Unit 4: Client limiter TTL 自动清理**

**Goal:** 定期清理不再活跃的 client limiter，防止内存无限增长

**Requirements:** R9

**Dependencies:** Unit 2

**Files:**
- Modify: `service/speedlimiter/manager.go`
- Test: `service/speedlimiter/manager_test.go`

**Approach:**
- `UserLimiter` 新增 `lastActive atomic.Int64` 字段，存储 Unix 时间戳
- 新增 `Touch()` 方法：`lastActive.Store(time.Now().Unix())`
- **只在 `GetOrCreateLimiterForClient` 返回 limiter 时调用 `Touch()`**，不在每次 Read/Write 中调用。原因：
  - `time.Now()` 有一定开销，每次网络 I/O 调用会累积
  - 每个新连接都经过 `GetOrCreateLimiterForClient`，活跃用户的 limiter 会被持续 Touch
  - TTL 设为 10 分钟，只要 10 分钟内有任何新连接就不会被清理，足够安全
  - `ThrottledConn` 不需要持有 `*UserLimiter` 引用，保持现有结构不变
- 复用已有的 `CheckSchedules` 循环（每分钟一次），在清理逻辑中：
  - `limiters.Range` 遍历所有条目
  - 仅处理复合 key（包含 `|`）的 client limiter
  - 如果 `now - lastActive > clientLimiterTTL`（默认 10 分钟），`limiters.Delete(key)`
  - 纯用户 key（不含 `|`）永不清理
- TTL 可配置（`SpeedLimiterDefault.ClientTTLMinutes`），默认 10 分钟

**Patterns to follow:**
- 已有的 `CheckSchedules`/`applyRuntimeStateLocked` 定时遍历模式
- sing-box 项目中 `atomic.Int64`/`atomic.Bool` 的使用模式

**Test scenarios:**
- Happy path: client limiter 超过 TTL 后被清理
- Happy path: 活跃连接的 client limiter 持续 Touch，不被清理
- Edge case: per_user 模式的 limiter（纯用户 key）永不被清理
- Edge case: TTL 边界 — 恰好等于 TTL 时不清理，超过才清理
- Integration: 清理后同一 client 重新连接，新 limiter 被正确创建

**Verification:**
- 模拟时间推进验证清理逻辑
- 确认活跃连接不受影响

---

- [ ] **Unit 5: 动态配置源支持 per_client**

**Goal:** Postgres/Redis 动态配置源的 `ConfigRow` 支持 `per_client` 字段

**Requirements:** R6

**Dependencies:** Unit 1

**Files:**
- Modify: `service/dynamicconfig/source_postgres.go`
- Modify: `service/dynamicconfig/source_redis.go`
- Modify: `service/speedlimiter/service.go` — `applyDynamic` 传递 `PerClient` 字段

**Approach:**
- `dynamicconfig.ConfigRow` 新增 `PerClient *bool` 字段
- Postgres source：SQL 查询增加 `per_client` 列（nullable boolean），映射到 `*bool`
- Redis source：hash field 增加 `per_client`，解析为 `*bool`
- `service.go` 的 `applyDynamic` 构建 `SpeedLimiterUser` 时传递 `PerClient` 字段

**Test scenarios:**
- Happy path: 从 Postgres/Redis 加载含 per_client=true 的配置，limiter 按 client 模式工作
- Edge case: per_client 列为 NULL/不存在时，fallback 到 default/group 设置

**Verification:**
- 编译通过
- 动态配置变更后 per_client 模式正确生效

## System-Wide Impact

- **Interaction graph:** 仅影响 speedlimiter service 内部。`ConnectionTracker` 接口不变，`RoutedConnection`/`RoutedPacketConnection` 签名不变
- **Error propagation:** 无新错误路径
- **State lifecycle risks:** per_client 模式下 limiter map 条目数量会随 client 数增长。单个 limiter 约 ~100 bytes，1000 个 client 也仅 ~100KB，可接受
- **API surface parity:** 动态配置 API (`ApplyConfig`/`RemoveConfig`) 需同步支持 `per_client` 字段
- **Unchanged invariants:** schedule 机制、group 机制、三级优先级合并逻辑的语义不变；仅 limiter 粒度可配置

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| NAT 后多设备共享源 IP 导致限速不准 | 文档说明此为已知限制；这是部署层面问题，不影响实现正确性 |
| client limiter 数量无限增长 | Unit 4 实现 TTL 自动清理（默认 10 分钟），复用 schedule loop 每分钟扫描，`atomic.Int64` 记录活跃时间零锁开销 |
| 复合 key 提取 userName | 使用 `strings.Cut(key, "|")` 提取，`|` 不出现在用户名和 IP 地址中（包括 IPv6），简单可靠 |
| 热路径性能 | 完全无锁：`atomic.Pointer` 读配置 + `compatible.Map.LoadOrStore` 查 limiter，无任何互斥锁 |
| `compatible.Map.LoadOrStore` 并发 miss 创建多余 limiter | `LoadOrStore` 保证只有一个 winner 被存储，loser 被 GC 回收，不影响正确性 |
| `configSnapshot` 原子替换期间正在执行的热路径读到旧配置 | 可接受：热路径读到的旧 snapshot 是一致的，下次调用会读到新配置，延迟最多一次连接 |
| 动态配置源缺少 per_client 字段 | Unit 5 扩展 `ConfigRow` 和 Postgres/Redis 查询，nullable boolean 映射为 `*bool` |

## Sources & References

- Related plan: `docs/plans/2026-04-07-001-feat-per-user-speed-limiter-plan.md`
- Core file: `service/speedlimiter/manager.go`
- Config: `option/speed_limiter.go`
- Metadata: `adapter/inbound.go:51` — `Source M.Socksaddr`
