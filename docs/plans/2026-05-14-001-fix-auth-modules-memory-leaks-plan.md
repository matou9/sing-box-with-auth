---
title: "fix: Plug memory leaks and unbounded growth in user-provider / traffic-quota / speed-limiter / admin-api"
type: fix
status: active
date: 2026-05-14
---

# fix: Plug memory leaks and unbounded growth in user-provider / traffic-quota / speed-limiter / admin-api

## Overview

近期新增的四个服务（`userprovider`、`trafficquota`、`speedlimiter`、`adminapi`）在静态审查中暴露出多处会导致进程内存稳态增长或在 reload 路径上无法释放的隐患。它们都不是"立即崩溃"型 bug，但在高并发、长时间运行、或频繁热加载场景下会逐步表现为 RSS 持续上涨。

本计划按"高/中/低"风险分级一次性收敛全部问题，分为八个独立单元，可并行实施、互不阻塞。所有修复都不引入新的配置字段，也不改变已有协议的语义。

**特别说明**：本计划的 Unit 3（`connList` 改造）涉及到一个用户特别关心的安全性问题——"删除错误是否会导致用户正常连接被断开"。该问题在 Unit 3 的 Approach / Safety Proof 章节做了逐条证明，结论是：`connList.remove` 的误操作在功能上**不会断开任何正常连接**，最坏后果是 quota 弱化（超限后多传几个包），方向与"误断"相反。

## Problem Frame

四个服务通过 `service/dynamicconfig` 共享一套"长跑后台 goroutine + 周期同步 + 路由器 ConnectionTracker"的运行模式。这种模式在三个边界容易出错：

1. **Goroutine 生命周期管理**：`go xxx()` 派生的后台循环没被纳入 `sync.WaitGroup` ⇒ Close 返回时仍在跑 ⇒ reload 场景出现 manager 副本累积。
2. **per-user / per-conn 容器无界增长**：`connList`、`limiters`、`userState`、`activeConns` 等容器在用户/连接结束后没有显式收缩底层数组，或者根本不清理。
3. **全量 IO 同步策略**：`flushPending → LoadAll(periodKey)` 之类"用增量做触发但用全量做对账"的模式在用户量增长时分配压力指数级放大。

这些问题在功能测试里看不见（短跑 + 少量用户 + 不 reload），需要在代码层面收敛。

## Requirements Trace

- R1. `speedlimiter` 的 `StartScheduleLoop` goroutine 必须能被 `Service.Close()` 同步等待退出
- R2. `userprovider` 的所有 source goroutine（fileSource/httpSource/redisSource/postgresSource + 内嵌 subscribe/listen）必须能被 `Service.Close()` 同步等待退出
- R3. `trafficquota.connList.remove` 必须在删除元素时清零原槽位接口引用，且 `connList.conns` 底层数组在长期低水位时能收缩
- R4. `trafficquota.flushPending` 不得在每次 flush 都对单周期做全表 `LoadAll`，应改为只查涉及到的用户名
- R5. `userprovider/source_http.fetch` 不得每次都新建 `*http.Transport`；至少 idle 连接要在 fetch 结束时释放
- R6. `speedlimiter` per-user 限流器（不含 `|`）在用户被显式 RemoveConfig 后必须被清理（已正确实现，需补回归测试守护）
- R7. `trafficquota.QuotaManager` 中的 `userState` / `activeConns` 在 RemoveConfig 后必须从 `compatible.Map` 中删除（已正确实现，需补回归测试守护）
- R8. 所有修复必须可在不改变服务对外行为的前提下完成，admin-api / 路由 / 协议层接口签名不变
- R9. 修复后必须新增针对每个 P0/P1 问题的回归测试，至少覆盖 goroutine 退出、底层数组复用、`LoadMany` 路径
- R10. 性能层面：修复后 `connList.remove` 的删除路径保持 O(n) 或更优；`flushPending` 的单次分配在用户量为 N 时应为 O(active_users) 而非 O(total_users)
- R11. `connList.remove` 的任何误操作（漏删 / 误删）**不得让正常连接被断开**——本要求由 swap-and-zero 实现的形式化安全证明（见 Unit 3）保障
- R12. `QuotaManager.RegisterConn` 与 `RemoveConfig` 并发执行时，不得出现"新建连接被加进已从 `activeConns` 摘除的 `connList`"的孤儿状态——该 conn 在未来的 `closeAll` 中将永远漏掉
- R13. `trafficquota` 的 pending flush 必须真正与总用户数解耦：不得只把 `LoadAll` 改为 `LoadMany`，还必须避免 `ConsumePendingDeltas` 每轮遍历全部配置用户；任何 dirty-user 索引设计都不得丢失 `AddBytes` 与 flush 并发产生的新 delta
- R14. `trafficquota.RemoveConfig` / `tripExceeded` 在关闭或标记连接时必须保持"锁内快照、锁内清引用、锁外 mark"的结构，避免 nil 非法访问、长时间持锁和 `sync.Map.Delete` lazy 释放导致的引用滞留
- R15. `Persister.LoadMany` 的 Redis / Postgres 实现必须有批大小上限，不能把 active users 全部塞进单次 `MGET` 或 `ANY($1)`，避免把全量扫描问题改造成单次大包内存尖峰
- R16. `HTTPSource` 复用 `http.Client` 时必须有并发保护；响应 JSON 必须有大小上限并保持原有 `json.Unmarshal` 的 trailing garbage 拒绝语义
- R17. `admin-api` 本身无状态泄漏面，但其 HTTP server 必须补齐 read/header/idle timeout 与请求体大小限制，避免管理面资源耗尽风险；路由和鉴权语义保持不变
- R18. 锁设计默认优先复用已有锁；只有当已有锁的 ownership 语义不匹配、会把内部 invariant 泄漏给调用方、或 p99 风险更高时，才新增局部锁或局部状态

## Scope Boundaries

- 不重构 service 注册/启动顺序，box.go 不动
- 不引入新的接口或抽象类型；`Persister` 接口只新增方法不删除既有方法
- 不动 admin-api 的 HTTP 路由和鉴权
- 不动 `compatible.Map` 实现（基于 `sync.Map`，跨 Go 版本一致，不在本计划范围）
  - **已知边界：** `sync.Map` 的 read map / dirty map 底层数组在大量 `Store`+`Delete` 抖动后**不会收缩**——这是 stdlib 限制，不是 sing-box 缺陷。本 plan 通过 Unit 3 `closeAllAndClear` + Unit 7 文档约束（用户名有界）缓解 value 残留与条目无界增长两类问题；但若用户**总 churn 量级 > 10 万**（典型场景：动态用户中心反复 add/remove 数十万账号），`activeConns` / `userState` 底层 `sync.Map` 的常驻字节数会显著高于稳态用户数所需。该症状不在本 plan 修复范围，需要单独 plan（候选方案：自实现 generic concurrent map with shrinking、或周期性 `sync.Map` 重建迁移）。
  - **判定方法：** 若用户报告"内存居高不降"且 `pprof heap` 显示 `sync.Map.dirty` / `sync.Map.read` 累计字节占比 > 10%，则属于本边界外问题。
- 不优化短命对象分配（如 `mergedUsersLocked` 重建 map）——属于 GC 压力优化而非泄漏，单独列入后续 plan
- 不引入对象池（`sync.Pool`），保持代码可读性优先
- 不动 `Authenticator` —— 该模块本身无状态，无泄漏面
- 不调整 token TTL 或登录会话语义

## Context & Research

### Relevant Code and Patterns

- `service/speedlimiter/service.go:65,117-121` —— `StartScheduleLoop(s.ctx)` 的派生 goroutine 不在 `s.wg` 中，`Close()` 不等待
- `service/speedlimiter/manager.go:533-548` —— schedule loop goroutine 实现
- `service/userprovider/service.go:108-119,151-154` —— 4 个 source goroutine 用裸 `go` 派生，`Close()` 仅 `cancel + common.Close(sources)`
- `service/userprovider/source_redis.go:84,102-115` —— 内嵌的 `go s.subscribe(ctx, onUpdate)` 不被任何 wg 等待
- `service/userprovider/source_postgres.go:100,119-132` —— 内嵌的 `go s.listen(ctx, onUpdate)` 同样无 wg
- `service/dynamicconfig/source_redis.go:69-91` —— **已正确**用本地 `sync.WaitGroup` 等待子 goroutine，可作模板
- `service/dynamicconfig/source_postgres.go:82-103,156-174` —— 同上，正确模板
- `service/userprovider/source_http.go:80-116` —— 每次 fetch 重建 `*http.Client` + `*http.Transport`，TLS / HTTP2 / idle 连接全部失效
- `service/trafficquota/manager.go:64-99` —— `connList` 用 slice + `append(s[:i], s[i+1:]...)` 删除，底层数组只增不缩，且未清零末位接口槽
- `service/trafficquota/manager.go:171-187` —— `RegisterConn` 通过 `LoadOrStore` 把 `connList` 存进 `activeConns`，依赖闭包 + `Close()` 触发 `remove`
- `service/trafficquota/conn.go:24-65` —— `QuotaConn.Close` 走 `closeOnce`，是 `onClose` 触发的唯一路径
- `service/trafficquota/service.go:351-411` —— `flushPending` 用 `LoadAll(periodKey)` 全周期拉取所有用户，再按本批 user 名字过滤
- `service/trafficquota/persist_redis.go:58-87` —— `LoadAll` 用 `SCAN` 拉取整周期 keys，规模 = quota 用户总数
- `service/trafficquota/persist_postgres.go:65-86` —— `LoadAll` 用 `SELECT user_name, bytes_used WHERE period_key = $1`，全周期全表扫
- `service/trafficquota/persist.go:1-41` —— `Persister` 接口与 `NoopPersister`，新增方法时需要在 4 个实现里同步
- `service/speedlimiter/manager.go:579-595` —— per-client 限流器有 TTL 清理；per-user 限流器（无 `|`）注释明确"never cleaned"
- `service/speedlimiter/manager.go:442-462` —— `RemoveConfig` 已正确清理两类 key
- `common/compatible/map.go:1-58` —— 基于 `sync.Map` 的泛型包装，`Len` 是 O(n)
- `box.go` —— Service 生命周期：`Initialize → Start → PostStart → Close`，`Close` 通过 `common.Close` 聚合错误

### Institutional Learnings

- `sync.Map` 的 `Delete` 是 lazy 的：read map 中的条目会保留到下次 dirty map 提升才真正释放。**不能依赖 `Delete` 立即让 GC 回收**——这意味着所有"删除"路径要确保 value 内部不再持有大对象引用（即把 value 的内部状态清空），而不是只删 key。
- `append(slice[:i], slice[i+1:]...)` 是 Go 中删除元素的标准写法，但**两个隐藏陷阱**：(1) 不清零末位槽 ⇒ 接口值/指针残留 ⇒ GC 看到"活引用"；(2) 底层数组不缩 ⇒ 历史峰值永久占用。这两个问题在审查里反复出现，应作为团队约定。
- `*http.Transport` 内部持有连接池、idle conn 的 reader/writer goroutine，**`response.Body.Close()` 只归还连接到池**，不关闭 idle 连接也不释放 Transport 自身。短命 Transport 是常见反模式。
- sing-box 的 `Service.Close` 不严格要求"返回前所有 goroutine 退出"，但**一旦上层做 reload**（admin-api runtime control 计划已经在 `docs/plans/.../admin-api-user-runtime-control` 里规划），新旧 manager 共存就会立刻暴露 goroutine 泄漏。修复要早于 reload 功能落地。

### External References

- `pgxpool` 文档：`pgxpool.Pool.Close()` 同步关闭所有连接，不在本计划内调整
- `redis/go-redis/v9` 文档：`Client.Close()` 异步关闭，订阅 channel 会被立即触达 `<-ch !ok`，验证关闭路径无需额外等待

## Key Technical Decisions

- **goroutine 泄漏统一用 `sync.WaitGroup` 拦截，不引入新抽象**
  - `speedlimiter.Service`：把 schedule loop 的 goroutine 显式 `s.wg.Add(1) / defer s.wg.Done()`，沿用现有 `wg` 字段
  - `userprovider.Service`：新增 `wg sync.WaitGroup` 字段，4 个 source goroutine 都包一层
  - source 内部嵌套的 subscribe/listen goroutine：在 source 自身的 `Run` 内用本地 `sync.WaitGroup`（dynamicconfig 已有正确模板）
  - **不用 errgroup**：本计划只关心退出，不关心错误传播；errgroup 反而增加上下文捕获面
- **`connList.remove` 改用 swap-and-zero 删除 + 周期性 shrink**
  - 删除：`l.conns[i] = l.conns[last]; l.conns[last] = nil; l.conns = l.conns[:last]`
  - shrink 策略必须保守：只有当 `cap(l.conns)` 已明显超过常态水位（建议初始阈值不低于 1024，最终由 benchmark 定型）且 `len(l.conns) < cap(l.conns)/4` 时才 copy 到新数组，避免连接波动时把 shrink copy 打到 p99 上
  - `closeAll` 路径新增 `closeAllAndClear` 语义：锁内复制快照、清零原 slice、`l.conns = nil`，锁外遍历快照调用 `markQuotaExceeded`；禁止在 `connList.access` 锁内调用可扩展回调
  - **为什么不切到 `map[quotaTrackedConn]struct{}`**：`closeAll` 需要稳定快照 + 顺序无关，map 增加 GC 压力且单元素开销大；slice + swap-and-zero 是常见 idiom，性能/可读性平衡
  - **为什么不用 `container/list`**：双链表节点开销大；本场景不需要保持插入顺序
- **`flushPending` 改 `LoadAll` 为 `LoadMany`，并新增不丢事件的 dirty-user 索引**
  - 仅替换 `LoadAll` 不足以满足 R10：当前 `ConsumePendingDeltas` 仍会遍历所有配置用户；必须让 flush 的第一阶段只消费 dirty users
  - dirty-user 设计不得使用简单 `sync.Map.Store` / drain 后 `Delete`，该模式会在 `AddBytes` 与 flush 并发时丢失下一轮 flush 事件
  - 推荐形态：`QuotaManager` 内维护 `dirtyMu + dirtySet + dirtyQueue`；`AddBytes` 先增加 `pendingDelta`，再在 `dirtyMu` 下把用户加入 set/queue；flush 在 `dirtyMu` 下整体摘走 queue 并清空 set。该协议允许并发 AddBytes 造成下一轮出现 delta=0 的空消费，但不得丢 delta
  - `ConsumePendingDeltas` 的返回值应从 `map[string]int64` 升级为带 period 的内部结构（如 `[]pendingDelta{user, periodKey, delta}`），避免 period reset 与 flush 并发时把旧周期流量写入新周期
  - `AddBytes`、`ConsumePendingDeltas`、`CheckPeriodReset` 中任何同时触及 `periodKey` 与 `pendingDelta` 的操作必须通过同一个 `userState.periodAccess` 临界区完成；这是复用已有锁，避免新增 per-delta 锁
  - `handlePeriodResets` 与 `flushPending` 的持久化 I/O 必须通过 `persistAccess` 串行化，避免 `Delete(previousKey)` 后又有迟到 flush 把旧周期记录写回来
  - 新增 `Persister.LoadMany(periodKey string, users []string) (map[string]int64, error)`
  - Redis 实现用 `MGET` 拉取 key，但必须按固定 batch 分批（建议 512 或 1000 起测）
  - Postgres 实现用 `WHERE user_name = ANY($1) AND period_key = $2`，同样必须分批，避免单次参数数组过大
  - NoopPersister 返回空 map
  - **保留 `LoadAll`**：`restoreUsage` 不需要它，但其他外部工具/测试可能依赖；标记 deprecated 但不删
  - **为什么不把 reconcile 完全去掉**：reconcile 的语义是"多实例同步：另一个 sing-box 也在 incr 同一个 key，本地需要拉回最新"——这是分布式部署的功能需求，不能直接去掉
- **`HTTPSource` 复用 `http.Client`**
  - 在 `HTTPSource` 上加 `client *http.Client` 字段
  - 同步加 `clientMu sync.Mutex` 或等价保护，覆盖 lazy init、`CloseIdleConnections`、`Close()` 与并发 `fetch` 的交错，`-race` 必须干净
  - 第一次 fetch 时根据 dialer 构造一次；dialer 不会在运行期变化（`downloadDetour` 是配置项）
  - Transport 明确设置 `IdleConnTimeout`、`MaxIdleConns`、`MaxIdleConnsPerHost`，让连接池有上界和自然回收策略
  - 响应体解析必须加大小上限；若从 `io.ReadAll + json.Unmarshal` 改为 streaming decoder，必须在第一次 Decode 后再 Decode 一次并要求 `io.EOF`，保持对 trailing garbage 的拒绝语义
  - `Close()` 调用 `client.CloseIdleConnections()`
  - **为什么不每次 fetch 后 `CloseIdleConnections()`**：会破坏 TLS session ticket / HTTP2 连接复用；用 idle timeout 自然回收即可
- **per-user 限流器的"永不清理"用注释 + 测试守护"约束生效"**
  - 不引入 TTL，保持热路径 lock-free
  - 在 `RemoveConfig` 测试里显式断言 `m.limiters.Len() == 0`
  - 在 README / plan 中注明"用户名必须有界"
  - **不修复**：当前实现已通过 `RemoveConfig` 清理，剩余风险只在"用户名无界"的非典型场景，靠文档约束更经济
- **回归测试用真实 `*Service.Close()` 路径**
  - 不 mock goroutine，启动真实 service 后 `Close()`，用 `runtime.NumGoroutine()` 差分断言（容忍 ±2 个抖动）
  - `connList` 收缩用 `cap(connList.conns)` 直接断言
  - `flushPending` 用 fake Persister 记录调用参数，断言 `LoadMany` 被调用、`LoadAll` 不被调用
- **`connList.remove` 选 swap-and-zero 而非保序删除**
  - 保序删除（`append(s[:i], s[i+1:]...)`）的 memmove 成本是 O(n-i)；swap 是 O(1)
  - `closeAll` 全量遍历，对顺序无依赖；调用方也不感知顺序
  - **关键安全性**：swap-and-zero 在锁内一气呵成，`closeAll` 拷贝快照永远看不到 nil 中间态（详见 Unit 3 Safety Proof P1）
  - **关键安全性**：故障模式（漏删/误删）只影响 quota 触发，不影响连接 Close 路径（Safety Proof P3）—— 这一点是用户最关心的"会不会断开正常连接"的形式化答案：**不会**
- **孤儿 conn race 选 QuotaManager 内部 lifecycle 保护，不复用 Service.applyMu**
  - 复用已有锁的前提是 ownership 语义匹配；`applyMu` 保护的是外部动态配置 apply/remove，而 `RegisterConn → activeConns → connList.add` 的一致性是 `QuotaManager` 的内部 invariant
  - 不把"调用 `RegisterConn` 前必须持有某个 Service 锁"作为隐藏契约；否则未来测试、admin-api 或其他调用方绕过 Service 时会重新引入孤儿 conn
  - 推荐在 `QuotaManager` 内增加极短持有的 lifecycle 锁，或 per-user/striped lifecycle 锁；`RemoveConfig` 不得在全局写锁下执行大用户 `closeAll`，避免阻塞所有用户连接注册
  - 如果 benchmark 证明 manager 内部锁比复用 `applyMu` p99 更低或封装性更强，则新增锁不视为冗余设计
- **admin-api 资源边界作为轻量补充，而不是重构**
  - `admin-api` 不纳入状态泄漏修复主链；只补 `http.Server` timeout 与 handler body limit
  - 不改变 `Authenticator`、token TTL、路由路径或 handler 行为

## Open Questions

### Resolved During Planning

- **Q: `speedlimiter.StartScheduleLoop` 是否能直接改成阻塞 + 由 `Service.Start` 派生？**
  → 不行。`Service.Start` 必须立即返回让 box 推进到下一阶段；schedule loop 是 fire-and-forget 后台任务。改 wg 包裹即可，无需重构接口。
- **Q: `userprovider` 的 4 个 source 是否需要按"先 subscribe 后 polling"的顺序退出？**
  → 不需要。`ctx.Cancel` 同时通知，所有 select 都会观察到，退出顺序无依赖。本地 wg 只需 `Add(N) → wait at end`。
- **Q: `connList.remove` 切到 swap-and-zero 后 `closeAll` 是否还能稳定迭代？**
  → 能。`closeAll` 先 `append(nil, l.conns...)` 拷贝出快照，再在锁外迭代调用 `markQuotaExceeded`。swap-and-zero 不改变迭代语义。
- **Q: `LoadMany` 引入后 `Persister` 接口的 4 个实现是否都要改？**
  → 是，但 `NoopPersister` / `RedisPersister` / `PostgresPersister` 都很短；stub 文件（`!with_redis` / `!with_postgres`）也要补占位。
- **Q: `HTTPSource` 缓存 `*http.Client` 后，配置热更（如未来加 `download_detour` 动态切换）怎么办？**
  → 当前没有配置热更路径；如未来出现，client 重建逻辑可单独加。本次不预先设计。
- **Q: 是否需要给 schedule loop 的退出加超时？**
  → 不需要。schedule loop 的 select 没有 IO 阻塞，`ctx.Done` 立即被观察到，wg 等待最多几微秒。
- **Q: Unit 3 的 swap-and-zero 是否会因为顺序改变导致 closeAll 的通知顺序不可预测？**
  → `closeAll` 的语义本就是"全量通知，顺序无关"；调用方 `tripExceeded` 不依赖顺序；`markQuotaExceeded` 是 atomic store，幂等。顺序变化无可观察副作用。
- **Q: Unit 3 误删（`remove` 不该删的删了）的影响是否会让用户的连接被强行断开？**
  → 不会。`closeAll` 漏掉的 conn 永远收不到 `markQuotaExceeded`，结果是这条 conn 继续按正常速率传数据（与"未被标记 quota 超限"的连接行为完全一致）。唯一的语义偏差是 quota 上限弱化，不影响连接可用性。详见 Unit 3 的 Safety Proof P3。
- **Q: Unit 6 选 Service.applyMu 复用，还是 QuotaManager 内部 lifecycle 保护？**
  → 选 QuotaManager 内部 lifecycle 保护。复用已有锁只有在 ownership 语义一致时才是简化；`applyMu` 属于 service 外部配置 apply/remove，`RegisterConn` 与 `RemoveConfig` 的孤儿 conn invariant 属于 manager 内部状态机。把锁放在 manager 内能避免隐藏调用约束，也能更容易做 per-user/striped 细粒度优化。
- **Q: Unit 6 是否会与 Unit 3 的 connList 锁产生死锁？**
  → 新锁顺序固定为：先 `QuotaManager` lifecycle 锁（或 per-user lifecycle 锁），后 `connList.access`。`connList.access` 锁内只能复制/清引用，不得调用 `markQuotaExceeded` 或其他可扩展回调；回调必须在所有 manager/connList 锁外执行。

### Deferred to Implementation

- `connList` shrink 阈值（`< cap/4` 还是 `< cap/2`、`cap > 1024` 还是 `cap > 4096`）：等实现时基准测试 5 万连接波动场景后定型；低阈值（如 32/64）默认视为 p99 风险，不作为初始值
- `Persister.LoadMany` 在 users 长度为 0 时的合约：返回空 map vs 返回 nil；倾向返回空 map 与 `LoadAll` 一致
- `Persister.LoadMany` 的 batch 大小：Redis 与 Postgres 起始建议 512 或 1000，最终以 `go test -bench -benchmem` 和真实 Redis/Postgres 参数限制定型
- dirty-user 的具体容器形态：优先选择 `QuotaManager.dirtyMu + dirtySet + dirtyQueue`，只有证明不会丢事件时才允许简化
- 是否给 `userprovider.Service` 也加 `wg.Wait()` 超时（如 30s）以防止 source goroutine 卡在 IO：先调整 Close 顺序为 `cancel → Close sources/transports → wg.Wait`；如 redis/pgx driver 仍有阻塞 bug，再加超时
- 测试是否要拉起真实 Redis/Postgres：用 `testcontainers` 太重，倾向于用 fake Persister 单元测试 + 现有 with_postgres / with_redis 集成测试维持

## High-Level Technical Design

> *This illustrates the intended approach and is directional guidance for review, not implementation specification. The implementing agent should treat it as context, not code to reproduce.*

### speedlimiter goroutine 收敛

```text
LimiterManager.StartScheduleLoop(ctx, wg):
  m.CheckSchedules(time.Now())
  wg.Add(1)
  go func():
      defer wg.Done()
      ticker := time.NewTicker(1 * time.Minute)
      defer ticker.Stop()
      for { select { case <-ctx.Done(): return; case t := <-ticker.C: m.CheckSchedules(t) } }

Service.Start:
  s.manager.StartScheduleLoop(s.ctx, &s.wg)
  s.startDynamicSources()

Service.Close:
  s.cancel()
  s.wg.Wait()
  return nil
```

### userprovider goroutine 收敛

```text
type Service struct {
  ...
  wg sync.WaitGroup
}

Service.Start:
  for each source in {file, http, redis, postgres}:
      if source != nil:
          s.wg.Add(1)
          go func():
              defer s.wg.Done()
              source.Run(s.ctx, s.onSourceUpdate)

Service.Close:
  s.cancel()
  // Close sources/transports first to unblock driver waits, then wait for Run loops.
  err := common.Close(s.httpSource, s.redisSource, s.postgresSource)
  s.wg.Wait()
  return err

RedisSource.Run:
  var wg sync.WaitGroup
  wg.Add(1)
  go func(): defer wg.Done(); s.subscribe(ctx, onUpdate)
  ... polling loop ...
  // on ctx.Done:
  wg.Wait()
  return

PostgresSource.Run: 同上
```

### trafficquota.connList 改造

```text
connList.remove(conn):
  lock
  for i, c := range l.conns:
      if c == conn:
          last := len(l.conns) - 1
          l.conns[i] = l.conns[last]   // swap
          l.conns[last] = nil           // zero out interface slot
          l.conns = l.conns[:last]
          // shrink if cap is way oversized
          if cap(l.conns) > highWatermark && len(l.conns) < cap(l.conns)/4:
              shrunk := make([]quotaTrackedConn, len(l.conns), len(l.conns)*2+1)
              copy(shrunk, l.conns)
              l.conns = shrunk
          return
  unlock

connList.closeAllAndClear():
  lock
  snapshot := append([]quotaTrackedConn(nil), l.conns...)
  clear(l.conns)
  l.conns = nil
  unlock
  for _, conn := range snapshot:
      conn.markQuotaExceeded()
```

### trafficquota dirty-user flush 索引

```text
QuotaManager:
  dirtyMu sync.Mutex
  dirtySet map[string]struct{}
  dirtyQueue []string

QuotaManager.AddBytes(user, n):
  state := m.stateFor(user)
  state.periodAccess.Lock()
  if state.periodKey == "":
      state.periodKey = currentPeriod
  state.usage.Add(n)
  state.pendingDelta.Add(n)
  state.periodAccess.Unlock()
  dirtyMu.Lock()
  if _, ok := dirtySet[user]; !ok:
      dirtySet[user] = struct{}{}
      dirtyQueue = append(dirtyQueue, user)
  dirtyMu.Unlock()

QuotaManager.ConsumePendingDeltas():
  dirtyMu.Lock()
  users := dirtyQueue
  dirtyQueue = nil
  dirtySet = make(map[string]struct{})
  dirtyMu.Unlock()
  for each user:
      state := load state
      state.periodAccess.Lock()
      periodKey := state.periodKey
      delta := state.pendingDelta.Swap(0)
      state.periodAccess.Unlock()
      if delta == 0:
          continue
      result append {user, periodKey, delta}
      // If AddBytes races after the dirty queue is drained, the user is queued
      // for the next flush. The current flush may consume that new delta too,
      // leaving a harmless zero-delta entry next round, but it must not lose it.

QuotaManager.CheckPeriodReset(now):
  for each user:
      state.periodAccess.Lock()
      previousKey := state.periodKey
      currentKey := periodKey(config, now)
      if previousKey != "" && currentKey != previousKey:
          state.pendingDelta.Swap(0) // discard old-period pending at boundary
          state.usage.Store(0)
          state.exceeded.Store(false)
          state.periodKey = currentKey
      state.periodAccess.Unlock()
```

### Persister.LoadMany

```text
Persister:
  LoadMany(periodKey string, users []string) (map[string]int64, error)
  // existing methods unchanged

RedisPersister.LoadMany:
  if len(users) == 0: return empty map
  for batch in chunks(users, loadManyBatchSize):
      keys := for each user: p.key(user, periodKey)
      values := p.client.MGet(ctx, keys...).Result()
      parse and merge into result map

PostgresPersister.LoadMany:
  for batch in chunks(users, loadManyBatchSize):
      query := "SELECT user_name, bytes_used FROM <table> WHERE period_key = $1 AND user_name = ANY($2)"
      rows := p.pool.Query(ctx, query, periodKey, batch)
      scan and merge into result map

NoopPersister.LoadMany:
  return empty map, nil

Service.flushPending step 3:
  loaded := make(map[string]map[string]int64)
  for periodKey, users := range periodUsers:
      result, err := s.persister.LoadMany(periodKey, users)
      ...
```

### HTTPSource client reuse

```text
HTTPSource:
  client *http.Client  // built lazily once
  clientMu sync.Mutex  // protects lazy init and CloseIdleConnections

HTTPSource.ensureClient(ctx) *http.Client:
  lock clientMu
  defer unlock
  if s.client != nil: return s.client
  // build with dialer + TLSConfig (existing logic)
  s.client = built
  return s.client

HTTPSource.fetch:
  httpClient := s.ensureClient(ctx)
  ... existing request/response handling ...
  decode JSON with a bounded reader
  decode a second token/value and require io.EOF to preserve trailing-garbage rejection

HTTPSource.Close:
  lock clientMu
  defer unlock
  if s.client != nil:
      s.client.CloseIdleConnections()
      // CloseIdleConnections also releases transport's idle goroutines
  return nil
```

## Implementation Units

- [ ] **Unit 1: speedlimiter — 把 schedule loop 纳入 WaitGroup**

**Goal:** `Service.Close()` 返回时 `StartScheduleLoop` 派生的 goroutine 已退出

**Requirements:** R1, R8, R9

**Dependencies:** 无

**Files:**
- Modify: `service/speedlimiter/manager.go`
- Modify: `service/speedlimiter/service.go`
- Test: `service/speedlimiter/service_test.go` (extend)

**Approach:**
- 修改 `LimiterManager.StartScheduleLoop(ctx context.Context)` 签名为 `StartScheduleLoop(ctx context.Context, wg *sync.WaitGroup)`
- 派生 goroutine 处 `wg.Add(1)` / `defer wg.Done()`
- 调用点 `service.go:65` 改为 `s.manager.StartScheduleLoop(s.ctx, &s.wg)`
- 不改变 `CheckSchedules` 行为，保持初始 `m.CheckSchedules(time.Now())` 在调用线程内执行

**Patterns to follow:**
- `service/trafficquota/service.go:88-90` —— `s.wg.Add(2); go func() { defer s.wg.Done(); s.runFlushLoop() }()` 模式
- `service/speedlimiter/service.go:83-87` —— 动态源已经在用同样的模式

**Test scenarios:**
- Happy path: 启动 `Service`、等待 200ms、调用 `Close()`、断言 `runtime.NumGoroutine()` 回到启动前水平（±2 容忍）
- Edge case: 调用 `Close()` 后立即返回，goroutine 已退出（用 channel 信号验证 schedule goroutine 的 defer 已经执行）
- Regression: 现有 `service_test.go` / `manager_test.go` 测试用例不退化
- **守护断言（trafficquota wg 完整性）：** 在 `service/trafficquota/service_test.go` 新增 `TestTrafficQuotaServiceCloseTerminatesAllLoops`，启动 `trafficquota.Service`（同时启用 dynamic redis/postgres 任一可用源 + flush loop + period reset loop），200ms 后 `Close()`，断言 `runtime.NumGoroutine()` 差分 ≤ ±2。`runFlushLoop` / `runPeriodResetLoop` / 动态 source goroutine 当前已在 `s.wg.Add(2)`（service.go:88-90）与 `s.wg.Add(1)`（service.go:108-112, 117-121），本断言**守护现有正确行为不在未来 PR 中被回归破坏**——属于 Unit 1 同级的 goroutine wg 完整性检查，不增加业务变更。

**Verification:**
- `cd test && go test -v -tags "..." -run "TestSpeedLimiter" ./...` 全部通过
- 新增 `TestSpeedLimiterCloseTerminatesScheduleLoop` 通过
- 新增 `TestTrafficQuotaServiceCloseTerminatesAllLoops` 通过（守护现有 wg 完整性）
- `make build` 通过

- [ ] **Unit 2: userprovider — source goroutine 统一 WaitGroup**

**Goal:** `Service.Close()` 返回时所有 source goroutine（顶层 + 内嵌 subscribe/listen）已退出

**Requirements:** R2, R8, R9

**Dependencies:** 无

**Files:**
- Modify: `service/userprovider/service.go`
- Modify: `service/userprovider/source_http.go`
- Modify: `service/userprovider/source_redis.go`
- Modify: `service/userprovider/source_postgres.go`
- Test: `service/userprovider/service_runtime_test.go` (extend)

**Approach:**
- `Service` 加 `wg sync.WaitGroup` 字段
- `Start` 中每个 `go xxx.Run(s.ctx, s.onSourceUpdate)` 包装为 `s.wg.Add(1); go func() { defer s.wg.Done(); xxx.Run(...) }()`
- `Close` 改为：`s.cancel(); closeErr := common.Close(...); s.wg.Wait(); return closeErr`，先关闭 source/client/pool/transport 以打断可能阻塞在 driver 内部的 goroutine，再等待 Run 循环退出
- `RedisSource.Run` 引入本地 `wg`：`wg.Add(1); go func() { defer wg.Done(); s.subscribe(...) }()`；在 ctx 取消的退出路径 `wg.Wait()`
- `PostgresSource.Run` 同上处理 `s.listen`
- `FileSource.Run` / `HTTPSource.Run` 无内嵌 goroutine，无需改动内部
- stub 文件（`!with_redis` / `!with_postgres`）的 `Run` 已是 no-op，无需改

**Patterns to follow:**
- `service/dynamicconfig/source_redis.go:67-91` —— 标准模板（已在用）
- `service/dynamicconfig/source_postgres.go:81-103,156-174`

**Test scenarios:**
- Happy path: 启动 `Service`（仅 fileSource）、Close、断言无残留 goroutine
- Happy path: 启动 `Service`（仅 httpSource，连接被拒绝的 URL）、Close、断言无残留 goroutine
- Edge case: redis/postgres source 启动后立即 Close（在 `time.After(5s)` reconnect 退避中）→ wg.Wait 应迅速返回（依赖 ctx.Done 被各 select 观察到）
- Regression: 现有 `service_runtime_test.go` 不退化

**Verification:**
- `cd service/userprovider && go test -v ./...`
- 新增 `TestServiceCloseTerminatesSourceGoroutines` 通过
- `make build` 通过

- [ ] **Unit 3: trafficquota — connList 改造（swap-and-zero + shrink）**

**Goal:** 长跑高峰过后 `connList` 底层数组占用回落，且不残留接口槽幻影引用

**Requirements:** R3, R8, R9, R10, R11

**Dependencies:** 无

**Files:**
- Modify: `service/trafficquota/manager.go`
- Test: `service/trafficquota/manager_test.go` (extend)

**Approach:**

- 引入包级常量：
  - `connListShrinkMinCap = 1024`（初始建议值，最终由 benchmark 定型）—— 低于此 cap 不做 shrink，避免小 list 抖动
  - `connListShrinkRatio = 4` —— 当 `len < cap/connListShrinkRatio` 触发 shrink
- `connList.remove` 改为 swap-and-zero 三步：
  1. `last := len(l.conns) - 1`
  2. `if i != last { l.conns[i] = l.conns[last] }` —— 把末位活跃元素挪到 i
  3. `l.conns[last] = nil; l.conns = l.conns[:last]` —— 清零原末位槽（释放接口值持有的 `*QuotaConn` 引用）并截断
- 在截断之后判断 shrink 条件：`cap(l.conns) > connListShrinkMinCap && len(l.conns) < cap(l.conns)/connListShrinkRatio` —— 满足则 `copy` 到 `make([]quotaTrackedConn, len, len*2+1)` 完成收缩
- 新增 `closeAllAndClear`：锁内拷贝快照、`clear(l.conns)`、`l.conns = nil`，锁外迭代 `markQuotaExceeded`；`RemoveConfig` 使用该路径确保 `sync.Map.Delete` lazy 时 value 内部也不再持有连接引用
- 保留 `closeAll` 的 "锁内拷贝快照 + 锁外迭代" 结构不变；如实现上合并为 `closeAllAndClear(clear bool)`，也必须保持锁外 mark
- 保留 `add` / `len` 不变（`add` 仍然 `append`，自然受益于 shrink 后较小的 cap）
- 整个 `remove` 函数体仍在 `l.access` 锁内执行

**Safety Proof（回答"删除错误会不会断开正常连接"的核心担忧）:**

> `connList` 的唯一对外副作用路径是 `closeAll → conn.markQuotaExceeded()`，且 `markQuotaExceeded` 只设置 atomic bool（`service/trafficquota/conn.go:63-65`），不直接关闭 socket。socket 是否被关、何时被关，由上层 `Read`/`Write` 看到 `closed=true` 返回 `ErrQuotaExceeded` 决定。因此**"`remove` 误操作"导致用户连接被断开的因果链不存在**。

证明分四条独立断言（任何一条被违反都需要修正本设计）：

- **P1 — closeAll 永不读到 nil**：
  - `closeAll` 锁内做 `append([]quotaTrackedConn(nil), l.conns...)`，拷贝的是 `l.conns[:len]` 这段 visible slice
  - swap-and-zero 把 nil 写入 `l.conns[last]`，但**在同一锁段内**立即 `l.conns = l.conns[:last]` 把这个 nil 槽从 visible slice 中切掉
  - mutex 提供 happens-before 保证：`closeAll` 拷贝时要么看到 swap 前的视图，要么看到 swap 后的视图，绝不会看到中间态
  - 结论：`closeAll` 迭代时不会对 nil 调用 `markQuotaExceeded` → 不会 panic
- **P2 — 接口值比较不可能"误删别人"**：
  - `quotaTrackedConn` 是接口，比较走 (type, value) 双等
  - 每条 `*QuotaConn` / `*QuotaPacketConn` 是堆上唯一指针
  - `l.conns[i] != conn` 在两条不同 conn 之间总为 true
  - 结论：`remove(connA)` 永远不会从 list 中拿掉 connB
- **P3 — 即使发生"漏删"或"误删"，也不能让正常连接被断开**：
  - 漏删（已关闭的 conn 仍残留在 list）：`closeAll` 会对它再调一次 `markQuotaExceeded` —— 该方法只设 `closed.Store(true)`，对已 Close 的 conn 是 no-op，**无可观察副作用**
  - 误删（活跃 conn 提前出 list）：`closeAll` 永远看不到它 —— 该 conn 在 quota 超限后**仍能继续读写**，方向是 "quota 弱化"，与 "连接被断开" 相反
  - 结论：`remove` 的故障模式只能让 quota 失效，不能让连接断开 ⇒ R11 满足
- **P4 — shrink 路径不丢失任何活跃 conn**：
  - `copy(shrunk, l.conns)` 中 `l.conns` 已经是截断后的 `l.conns[:last]`
  - `make([]quotaTrackedConn, len(l.conns), len(l.conns)*2+1)` 的 len 与 src 一致，copy 完整复制每一项
  - 旧底层数组在赋值 `l.conns = shrunk` 后无任何引用，**整块**被 GC 回收（不是逐槽清零）
  - 结论：shrink 不引入数据丢失

**Patterns to follow:**

- Go 1.21+ `slices.Delete` 会清零尾部但保持顺序，删除成本仍是 O(n-i)；本场景为了 O(1) 移除成本选择手写 swap-and-zero
- 现有 `closeAll` 锁外迭代的设计

**Test scenarios:**

- TC1 (P1 / P3): 加 100 条 conn，随机顺序 remove 一半，对剩余 50 条 `closeAll`，断言"被 remove 的 50 条未被 markQuotaExceeded、剩余 50 条全部被 markQuotaExceeded"——这是 R11 的直接回归测试
- TC2 (P4): 加入 1 万条、删除 9900 条后断言 cap 明显回落（阈值由最终 `connListShrinkMinCap` 决定），证明 shrink 有效
- TC3 (P1): 用 `unsafe.Pointer` 或在 manager.go 加 `//go:build test_only` 辅助函数读取底层数组 `[len(l.conns):cap(l.conns)]` 区段，断言相关槽位为 nil
- TC4 (P2): remove 一个**从未加入**的 conn，断言 list 未变（原行为）
- TC5: 并发 `add` + `remove` + `closeAll` 100 协程，`-race` 下不报警、无 panic
- TC6 (shrink 不抖动): 反复 add 50 / remove 40 循环 100 次，断言 cap 不无限增长也不频繁 shrink（cap 应稳定在某个低水位上下）
- Benchmark: `BenchmarkConnListRemove` 与改造前对比，删除路径仍 O(n) 查找，常数因子下降（swap 比 `append(s[:i], s[i+1:]...)` 的 memmove 更便宜）

**Verification:**

- `cd service/trafficquota && go test -race -v -run "TestConnList" ./...`
- `cd service/trafficquota && go test -bench=BenchmarkConnList -benchmem`
- 新增 `TestConnListRemoveDoesNotAffectOtherConns` / `TestConnListShrinksUnderwater` / `TestConnListRemoveZerosLastSlot` / `TestConnListCloseAllAndClearDropsReferences` / `TestConnListConcurrentAddRemoveCloseAll` 通过
- `make build` 通过

- [ ] **Unit 4: trafficquota — dirty-user flush + Persister.LoadMany 替换 LoadAll 热路径**

**Goal:** `flushPending` 单次 CPU/分配大小为 O(dirty_users)，与 quota 总用户数解耦

**Requirements:** R4, R8, R9, R10, R13, R15

**Dependencies:** 无

**Files:**
- Modify: `service/trafficquota/persist.go`
- Modify: `service/trafficquota/persist_redis.go`
- Modify: `service/trafficquota/persist_redis_stub.go`
- Modify: `service/trafficquota/persist_postgres.go`
- Modify: `service/trafficquota/persist_postgres_stub.go`
- Modify: `service/trafficquota/manager.go`
- Modify: `service/trafficquota/service.go`
- Test: `service/trafficquota/persist_test.go` (extend)
- Test: `service/trafficquota/service_test.go` (extend)
- Test: `service/trafficquota/manager_test.go` (extend)

**Approach:**
- 在 `QuotaManager` 中新增 `dirtyMu`、`dirtySet`、`dirtyQueue`，由 `AddBytes` 在成功累加 pending 后入队；`ConsumePendingDeltas` 先整体摘走 dirty queue，再只处理这些用户
- `ConsumePendingDeltas` 返回内部结构（如 `[]pendingDelta{user, periodKey, delta}`），不再返回 `map[string]int64`
- `AddBytes`、`ConsumePendingDeltas`、`CheckPeriodReset` 复用现有 `userState.periodAccess`，把 `periodKey` 与 `pendingDelta` 的读取/交换放进同一临界区，避免旧周期 delta 写入新周期
- `handlePeriodResets` 的持久化删除与 `flushPending` 复用 `Service.persistAccess` 串行化，避免旧周期 `Delete` 与迟到 flush I/O 互相复活记录
- `flushPending` 先按 `periodKey` 聚合 `pendingDelta`，再调用 `LoadMany(periodKey, users)`
- `Persister` 接口新增 `LoadMany(periodKey string, users []string) (map[string]int64, error)`
- `NoopPersister.LoadMany`：`return map[string]int64{}, nil`
- `RedisPersister.LoadMany`：
  - 空 `users` 时直接返回空 map
  - 按固定 batch 构造 key 列表，避免单次 `MGET` 过大
  - 对每个 batch 调用 `p.client.MGet(p.ctx, keys...).Result()`
  - 按下标对齐 `users[i] → values[i]`，nil 跳过，非数字字符串返回 error
- `PostgresPersister.LoadMany`：
  - 空 `users` 时直接返回空 map
  - 按固定 batch 使用 `WHERE period_key = $1 AND user_name = ANY($2)`，`pgx` 支持 `[]string` 自动转 `text[]`
  - 遍历 `rows.Next()` 拼接 map
- stub 文件：保持 NoopPersister 行为（如已有 stub 直接调用 noop，加一行 LoadMany 转发即可）
- `Service.flushPending` step 3：把 `s.persister.LoadAll(periodKey)` 替换为 `s.persister.LoadMany(periodKey, periodUsers[periodKey])`
- 保留 `LoadAll` 在 `Persister` 接口中（暂不删除），但在 service.go 内不再调用

**Patterns to follow:**
- 现有 `LoadAll` 的错误处理 / `redis.Nil` 跳过逻辑
- pgx 的 `ANY($1)` 习惯用法，参考 `service/userprovider/source_postgres.go`

**Test scenarios:**
- Happy path: Noop / Redis / Postgres 三种 Persister 都对 `LoadMany("2026-05-14", ["alice","bob"])` 返回正确 map
- Edge case: `users == nil` 或 `len == 0` → 返回空 map、不发起 IO
- Edge case: Redis 中部分 key 不存在（`redis.Nil`） → 跳过，不报错
- Edge case: Postgres 中部分 user 不存在 → 缺失的不出现在结果 map 中
- Race regression: 并发 `AddBytes` 与 `ConsumePendingDeltas` 不丢 delta，允许下一轮出现 zero-delta 空消费
- Race regression: `CheckPeriodReset` 与 `flushPending` 并发时，旧周期 pending 不会被写入新周期，也不会在 previous period delete 后被迟到 flush 重新写回
- Performance regression: 10 万配置用户、10 个 dirty 用户时，`ConsumePendingDeltas` 不遍历全部用户
- Regression: `service_test.go` 中 `flushPending` 路径的现有断言（多用户递增、错误重试）不退化
- Integration: fake Persister 记录调用历史，断言 `flushPending` 调用 `LoadMany` 而非 `LoadAll`

**Verification:**
- `cd service/trafficquota && go test -v ./...`（包含 `with_redis` / `with_postgres` 两组 build tag 各跑一遍）
- 新增 `TestRedisPersisterLoadMany` / `TestPostgresPersisterLoadMany` / `TestFlushPendingUsesLoadMany` 通过
- `make build` 通过

- [ ] **Unit 5: userprovider — HTTPSource 复用 http.Client**

**Goal:** `HTTPSource.fetch` 不再每次创建 `*http.Transport`；`Close` 释放 idle 连接

**Requirements:** R5, R8, R9, R16

**Dependencies:** 无

**Files:**
- Modify: `service/userprovider/source_http.go`
- Test: `service/userprovider/service_runtime_test.go` (extend if practical) 或独立的 source_http_test.go

**Approach:**
- `HTTPSource` 新增字段 `client *http.Client`
- `HTTPSource` 新增字段 `clientMu sync.Mutex`，保护 lazy init、`CloseIdleConnections` 和并发 Close/fetch 交错；不复用 `access`，因为 `access` 只保护 `cachedUsers`
- `HTTPSource` 新增字段 `closed bool`（受 `clientMu` 保护）—— 避免 `Close()` 之后 fetch 又通过 `ensureClient` 的 lazy init 路径**重新构造一个无人回收的 transport**，重新引入 goroutine 泄漏
- 拆出 `ensureClient(ctx) (*http.Client, error)` 方法：
  - 持 `clientMu` 检查 `closed`，已关闭则返回 `errHTTPSourceClosed`（包内定义的哨兵错误）
  - `s.client != nil` 直接返回；否则第一次调用时按 `downloadDetour` + outbound manager 构建（沿用现有逻辑），之后复用
  - 该哨兵错误与现有 `errHTTPSourceClosed` 命名风格保持一致；与 `source_redis` / `source_postgres` 的 Close 后拒绝 IO 语义对齐
- 自定义 Transport 设置 `IdleConnTimeout`、`MaxIdleConns`、`MaxIdleConnsPerHost`；具体数值跟随 Go `http.DefaultTransport` 的保守默认或略小值
- `fetch` 调用 `s.ensureClient(ctx)` 替代当前的内联构造；`errHTTPSourceClosed` 应在 `fetch` 上沿当作正常退出处理（不写入 `cachedUsers`、不报错日志降噪）
- `fetch` 的响应体解析加大小上限；如改用 `json.Decoder`，必须在解完主 JSON 后再次 Decode 并要求 `io.EOF`，保持 `json.Unmarshal` 对 trailing garbage 的拒绝语义
- 修改 `Close()`：在 `clientMu` 下置 `s.closed = true`；若 `s.client != nil`，调用 `s.client.CloseIdleConnections()`；幂等（重复 Close 安全）
- 保留 `http.DefaultClient` 兜底分支（dialer 为 nil 时）；该分支不需要缓存，但 `closed` 检查仍生效

**Patterns to follow:**
- `service/userprovider/source_postgres.go` 中 `s.pool` 字段的缓存与 `Close` 释放

**Test scenarios:**
- Happy path: 调用 `fetch` 两次，断言两次拿到同一个 `*http.Client`（用反射或 testing-only getter）
- Happy path: `Close()` 调用后再 `fetch`，`ensureClient` 返回 `errHTTPSourceClosed`；不重建 transport、`runtime.NumGoroutine()` 无新增（直接测试 closed 不变量，避免补强前的"行为未定义"灰区）
- Happy path: `Close()` 调用两次（幂等性），第二次不 panic、不重复 `CloseIdleConnections`
- Race regression: `fetch` 与 `Close` 并发，`go test -race` 不报警；并发场景下，Close 之后的 fetch 调用 100% 拿到 `errHTTPSourceClosed`
- Edge case: 超过大小上限的响应返回错误，不更新 `cachedUsers`
- Edge case: 合法 JSON 后带 trailing garbage 的响应返回错误，保持旧 `json.Unmarshal` 语义
- Regression: 现有依赖 HTTP source 的测试不退化
- Manual: pprof 截图——长跑 1 小时后，goroutine profile 中 `net/http.(*persistConn).readLoop` 数量稳定，不随 fetch 次数增长（不入自动化，仅 Verification 步骤手验）

**Verification:**
- `cd service/userprovider && go test -race -v ./...`
- `make build` 通过

- [ ] **Unit 6: trafficquota — RegisterConn 与 RemoveConfig 的孤儿 conn 修复**

**Goal:** 当 `RegisterConn` 与 `RemoveConfig` 并发执行时，新建连接不会被加进一个**已经从 `activeConns` 中摘除**的 `connList`，从而成为永远不会被 `closeAll` 通知到的孤儿。

**Requirements:** R8, R9, R12

**Dependencies:** 与 Unit 3 无相互依赖；可并行，也可合入同一 PR

**Files:**
- Modify: `service/trafficquota/manager.go`
- Modify: `service/trafficquota/service.go`（仅在需要把锁外移时）
- Test: `service/trafficquota/manager_test.go` (extend)

**Problem Recap:**

当前 `RegisterConn` (`manager.go:171-187`) 与 `RemoveConfig` (`manager.go:211-222`) 在不同锁下运行，存在如下交错：

```text
goroutine A (RegisterConn for "alice")          goroutine B (RemoveConfig for "alice")
-----------------------------------------       -----------------------------------------
loadConfig("alice") → loaded
stateFor("alice")
activeConns.LoadOrStore("alice", &connList{})
   → 拿到引用 L1
                                                deleteConfig("alice")
                                                activeConns.Load("alice") → L1
                                                L1.closeAll()        // 此时 A 还没 add
                                                activeConns.Delete("alice")
                                                states.Delete("alice")
L1.add(connA)
   // connA 加入了一个已不在 activeConns map 中的 connList
   // 之后 quota 不会再触发对 connA 的 closeAll
   // connA 成为永远漏掉的孤儿
```

后果：

- 不是内存泄漏（connA 自身会在用户主动关闭时被 GC），但**违反 quota 语义**
- 如果之后该用户再被 ApplyConfig 重建，新 connList 与 L1 完全不同，孤儿状态不可恢复

**Approach（最终选择）：QuotaManager 内部 lifecycle 保护**

- 不复用 `Service.applyMu`。`applyMu` 守护的是外部配置变更入口，不能成为 `QuotaManager.RegisterConn` 的隐藏调用前置条件。
- 在 `QuotaManager` 内新增 lifecycle 保护，优先考虑 per-user/striped 锁；如果实现复杂度过高，可先用全局 `registerAccess sync.RWMutex`，但写锁持有时间必须极短。
- `RegisterConn` 在 lifecycle 读侧完成 `loadConfig → stateFor → activeConns.LoadOrStore → connList.add`，保证它不会把新 conn 加进一个已经被 `RemoveConfig` 摘除的旧 `connList`。
- `RemoveConfig` 在 lifecycle 写侧只完成 `deleteConfig → activeConns.LoadAndDelete → states.Delete` 这类 map 状态切换；拿到的 `connList` 在锁外执行 `closeAllAndClear`，避免一个大用户删除阻塞所有用户新连接。
- `connList` 可以增加 `closed atomic.Bool` 或 `closed bool`（受 `connList.access` 保护），让晚到的 `remove` / `add` 能明确识别已关闭列表；如果 lifecycle 锁已经证明 add/remove 不会晚到旧 list，该字段可不加。

**Detailed Steps:**

- `Service.applyMu` 保持 `sync.Mutex`，不升级为 `sync.RWMutex`。
- `Service.RoutedConnection` 与 `RoutedPacketConnection` 保持只调用 `s.manager.RegisterConn(...)`，不承担 manager 内部锁约束。
- `QuotaManager` 新增 lifecycle 同步字段（命名 `lifecycle sync.RWMutex` 或 striped 等价物），并在 `RegisterConn` / `RemoveConfig` 内部使用。
- `RemoveConfig` 使用 `LoadAndDelete` 获取旧 `connList`，随后锁外调用 `cl.closeAllAndClear()`。
- 新增内部不变量注释："QuotaManager owns the RegisterConn/RemoveConfig lifecycle invariant; callers must not be required to hold Service locks."

**Lock Order Contract（强制约束，所有进入 `connList.access` 的路径必须遵守）：**

全局锁顺序：`QuotaManager.lifecycle` (R/W) → `userState.periodAccess` → `connList.access` → （锁外）`*QuotaConn.markQuotaExceeded` / `closeOnce`

不允许任何路径反向获取锁。下表枚举所有可能进入 `connList.access` 的入口：

| 入口路径 | 触发点 | 必须持有的外层锁 | 是否调用回调（必须锁外） |
|----------|--------|------------------|--------------------------|
| `QuotaManager.RegisterConn` → `connList.add` | 新连接注册 | `lifecycle.RLock()` | 否 |
| `QuotaManager.RemoveConfig` → `LoadAndDelete` → `connList.closeAllAndClear` | 动态删除用户 | `lifecycle.Lock()` 仅覆盖 `LoadAndDelete`；`closeAllAndClear` 在 `lifecycle` 释放后执行 | 是（`markQuotaExceeded` 在 `closeAllAndClear` 的锁外阶段） |
| `QuotaConn.Close` 的 `onClose` 回调 → `connList.remove` | 单连接关闭 | **无外层锁**（回调可能在 router/proxy 任意 goroutine 触发） | 否 |
| `QuotaManager.tripExceeded` → `connList.closeAll` | usage 超限 | 不持 `lifecycle`（usage 路径不能阻塞配置变更）；`closeAll` 内部短持 `connList.access` 做快照 | 是（`markQuotaExceeded` 锁外） |

**为什么 `onClose → remove` 不需要外层锁：** 该路径只修改自己所在 `connList` 的内部 slice，不读 `activeConns` map、不触发回调；即使 `RemoveConfig` 已并发摘除该 list，`remove` 操作的是 list 实例本身（指针由 `*QuotaConn` 闭包持有），不会与 lifecycle 写锁形成依赖回路。

**为什么 `tripExceeded` 不持 `lifecycle`：** usage 超限是热路径，若每次 `AddBytes` 超限都要拿 `lifecycle.RLock`，会与 `RemoveConfig` 的 `lifecycle.Lock` 互相延迟；`tripExceeded` 只通过 `activeConns.Load` 获取 list 引用，拿到的可能是已被 `LoadAndDelete` 摘除的旧 list——对该 list 调 `closeAll` 是幂等的（要么 list 已空、要么对其中的 conn 多调一次 `markQuotaExceeded` 也是 no-op，见 Unit 3 Safety Proof P3）。

**死锁防御：** `connList.access` 是叶子锁——其临界区内**只能**执行 slice 操作（`append`/swap/`clear`/`copy`），**禁止**调用 `markQuotaExceeded`、`onClose`、任何外部接口、任何可能再次获取 manager/service 锁的方法。所有回调都必须在 `closeAllAndClear` / `closeAll` 拷贝快照并释放 `access` 之后执行。这一约束已在 Unit 3 与本 Unit 共同强制。

**Patterns to follow:**

- 复用已有锁的判断标准：锁的 ownership、保护的数据集合、调用边界必须一致；仅仅"已有一个锁"不构成复用理由
- `service/trafficquota/service.go` 的 `persistAccess` 继续只保护持久化 I/O 与 RemoveConfig/flush 的交错，不扩展到连接注册热路径

**Test scenarios:**

- TC1 (孤儿场景重现，期望已修复)：
  - 100 协程并发对同一用户调用 `RegisterConn`，另起 1 协程在 50ms 后调 `RemoveConfig`
  - 收集所有 100 个 `*QuotaConn` 实例
  - 等 RemoveConfig 完成后断言：所有在 RemoveConfig 之前完成 add 的 conn 都已被 `markQuotaExceeded`（即 `closeAll` 路径覆盖到了）
  - 在 RemoveConfig 之后启动的 RegisterConn 应直接返回 nil/nil（因为 `loadConfig` 已 false）
- TC2: RegisterConn 与 RoutedConnection 在同一连接生命周期内的正常路径，确认 manager 内部 lifecycle 锁不破坏现有行为
- TC3: 高并发压测 `BenchmarkRoutedConnection`，确认 lifecycle 锁后无显著性能回退（< 5%）；同时增加 RemoveConfig 大用户场景，确认不会全局长时间阻塞其他用户注册
- TC4: `-race` 下跑 TC1，确认无报警

**Verification:**

- `cd service/trafficquota && go test -race -v -run "TestRegisterConnRemoveConfigOrphan" ./...`
- `cd service/trafficquota && go test -bench=^BenchmarkRegisterConn -benchmem -count=10 -cpu=8` 对比 `BenchmarkRegisterConn` / `BenchmarkRegisterConnParallel` 与对应的 `*NoLifecycle` peer
- `make build` 通过
- 原有 `service_test.go` / `manager_test.go` 全套通过

**Benchmark baseline (2026-05-26, Intel i9-13900HX, Go 1.24.13, CGO_ENABLED=0):**

`manager_bench_test.go` 通过 `registerConnNoLifecycle` 内嵌一个"pre-Unit-6 peer"路径，使我们可以在同一二进制内做 apples-to-apples 对比；`benchstat -count=10 -cpu=8` 显示 Unit 6 锁不仅没有引入计划设定的 5% 回退，在测得范围内反而有小幅性能 *改善*（lifecycle 释放后的连续路径减少了调度抖动）。

| Benchmark | Pre-Unit-6 (NoLifecycle) | Post-Unit-6 (WithLifecycle) | 变化 | p 值 |
|-----------|-------------------------|-----------------------------|------|------|
| `BenchmarkRegisterConn-8` (serial) | 706.2 ns/op ± 25% | 625.4 ns/op ± 4% | **-11.45%** | 0.000 |
| `BenchmarkRegisterConnParallel-8` (b.RunParallel) | 1059.0 ns/op ± 20% | 883.9 ns/op ± 6% | **-16.53%** | 0.029 |

B/op 与 allocs/op 在两个变体之间完全一致（216 B / 8 allocs），证明 Unit 6 没有引入额外分配。

**最坏情况上界**（Risks 中 "全局 lifecycle 写锁导致一个大用户 RemoveConfig 阻塞所有用户 RegisterConn"）：`BenchmarkRegisterConnUnderRemoveConfigChurn-8` 在背景 goroutine 以 ~180K-300K ops/sec 速率 ApplyConfig/RemoveConfig 同一个独立用户时，目标用户的 RegisterConn 时延仍稳定在 ~1340 ns/op。该 churn 速率比生产环境管理面操作高 4-5 个数量级，因此此值是合成上界而非现实负载预期；若未来确实出现 RemoveConfig 高频抖动场景，可按计划 Risks 的回退方案改为 per-user/striped lifecycle 锁。

**结论：5% p99 回退预算（计划 TC3）满足；无需切换为 striped lock。**

- [ ] **Unit 7: 文档与运维注释**

**Goal:** 把 P2 等级的"已正确实现，但需要约束"问题用文档/注释固化

**Requirements:** R6, R7

**Dependencies:** 无

**Files:**
- Modify: `service/speedlimiter/manager.go`（注释加强）
- Modify: `service/trafficquota/manager.go`（注释加强）
- Modify: `docs/auth-features-README.md`

**Approach:**
- `speedlimiter/manager.go:584-595` 的现有注释扩充："per-user 限流器永不 TTL 清理。用户名必须有界——通常通过 user-provider 上游控制。如果用户名无界（罕见），考虑改为 per-client 模式或加入 RemoveConfig 路径"
- `trafficquota/manager.go` 的 `userState` / `activeConns` 加注释："只在 RemoveConfig 时删除。用户从未发送过流量不会创建条目；发过流量后即使 quota 为 0 也保留"
- `docs/auth-features-README.md` 在运维章节补一条："`user_provider` 引用的用户名集合应保持有界。每个独立用户在 sing-box 进程中保留一份 `*UserLimiter` 与 `*userState`（量级各几十字节）。如计划支持数十万动态用户，请与维护者沟通容量评估"

**Patterns to follow:**
- 现有 README 的"运维说明"风格

**Test scenarios:** N/A（纯文档/注释）

**Verification:**
- 文档构建通过（如使用 mkdocs，可手动渲染检查）
- 注释经过 `make fmt` 不被改写

- [ ] **Unit 8: admin-api — HTTP server 资源边界**

**Goal:** admin-api 不新增状态存储，也不改变路由/鉴权，只为管理面 HTTP server 补资源边界，避免慢请求或超大 body 带来的资源占用风险。

**Requirements:** R8, R17

**Dependencies:** 无

**Files:**
- Modify: `service/adminapi/service.go`
- Modify: `service/adminapi/handler_user.go`
- Modify: `service/adminapi/handler_quota.go`
- Modify: `service/adminapi/handler_speed.go`
- Test: `service/adminapi/*_test.go` (extend)

**Approach:**
- `http.Server` 增加保守 timeout：`ReadHeaderTimeout`、`ReadTimeout`、`IdleTimeout`；不设置过短值，避免慢管理客户端误伤
- 对所有 decode JSON body 的 handler 使用 `http.MaxBytesReader` 或同等 limit；limit 必须足够覆盖批量 user create/update 的合理请求大小
- 不引入 session map、token revocation store 或后台清理 goroutine；`Authenticator` 继续保持无状态 signed token / static token 语义

**Redundancy decision:**
- 不新增全局锁或状态容器；admin-api 的问题是资源边界，不是内存泄漏状态机
- 不复用其他服务的锁；handler 只通过现有 controller/provider 接口进入对应服务

**Test scenarios:**
- Edge case: 超过 body limit 的 user/quota/speed 请求返回 4xx，服务继续可用
- Edge case: 正常大小请求行为不变
- Regression: login bearer flow、static token、basic auth 行为不变

**Verification:**
- `cd service/adminapi && go test -v ./...`
- `make build` 通过

## System-Wide Impact

- **Interaction graph:** `box.New → service.Start → service.Close` 的总体顺序不变。Unit 1/2 让 `Close()` 真正"同步"，对其他 service 的关闭时序无影响（都是独立 goroutine）。Unit 3/4/6 在 trafficquota 内部完成，对外接口零变化（`Persister.LoadAll` 暂不删，避免外部破坏）。Unit 5 在 HTTPSource 内部完成。admin-api 只补 HTTP server timeout/body limit，不改路由。
- **Error propagation:** `Persister.LoadMany` 的错误返回与 `LoadAll` 同语义；`flushPending` 在错误时仍走 `RestorePendingDelta` 回滚路径。`HTTPSource.ensureClient` 不产生新错误。
- **State lifecycle risks:** Unit 1/2 修复后，reload 路径上不再有 goroutine 副本。Unit 3 修复后，`connList` 在删除元素后立即放弃接口槽引用，闭包 / `*QuotaConn` 可被 GC 回收。Unit 4 减少单次 flush 的临时 map 大小。Unit 5 让 idle conn / transport goroutine 数量与 fetch 次数解耦。
- **API surface parity:** `Persister` 接口扩展（新增 LoadMany），但 4 个实现都同步更新；新方法对外不可见（接口未导出到 sing-box 外）。`adapter.ConnectionTracker` 接口完全不动。`adapter.Service` 接口完全不动。`adminapi` 路由完全不动。
- **Integration coverage:** Unit 1/2 的 goroutine 退出测试用 `runtime.NumGoroutine()` 差分，必须在 race detector 下跑通；Unit 3 的 shrink 行为用 `cap()` 直接断言；Unit 4 的 LoadMany 用 fake Persister 验证调用替换。
- **Unchanged invariants:**
  - `ConnectionTracker.RoutedConnection` / `RoutedPacketConnection` 行为不变
  - `LimiterManager.GetOrCreateLimiterForClient` 热路径锁与原子语义不变
  - `QuotaManager.AddBytes` 语义不变；实现上允许增加 dirty enqueue 与 period/pending 的短临界区，但必须用 benchmark 守护 p99
  - `Persister.LoadAll` 接口签名不动（暂保留）
  - admin-api 路由与鉴权不动；handler 只允许加 body limit，不改请求/响应语义
  - `userprovider` 的 ManagedUserServer 推送逻辑不动

## Redundancy Review

| Candidate | Reuse existing? | Decision |
|-----------|-----------------|----------|
| `trafficquota` RegisterConn/RemoveConfig lifecycle | 不复用 `Service.applyMu` | `applyMu` 属于 service 外部配置入口，不能承载 manager 内部 invariant；新增 manager 内部 lifecycle 保护不是冗余 |
| `trafficquota` pending dirty-user 索引 | 复用 `userState.periodAccess`，新增 `dirtyMu` | `periodAccess` 已保护 `periodKey`，应扩展为 period/pending 原子快照；dirty queue 是新数据结构，现有 `persistAccess` 保护 I/O，不适合热路径入队 |
| `connList` 内部保护 | 复用 `connList.access` | add/remove/snapshot/clear 均属于同一 slice ownership，复用现有 mutex；不新增 per-list 锁 |
| `flushPending` 与 RemoveConfig 持久化交错 | 复用 `Service.persistAccess` | 该锁已专门保护持久化删除与 flush 的交错，继续复用，避免新增 service 级 I/O 锁 |
| `HTTPSource` client 复用 | 新增 `clientMu` | 现有 `access` 保护 `cachedUsers`，不应复用来保护 transport/client 生命周期；新增锁粒度更小，避免阻塞 CachedUsers |
| `admin-api` 资源边界 | 不新增全局状态 | 只设置 `http.Server` timeout 与 handler body limit，不引入 session map 或 token store |

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| WaitGroup 包裹后 `Close` 可能因为 redis/pgx driver bug 永远不返回 | `userprovider.Close` 采用 `cancel → Close sources/transports → wg.Wait` 顺序先打断阻塞 I/O；如仍出现回归，下一步加 `wg.Wait()` 的 select-with-timeout |
| swap-and-zero 删除改变了 closeAll 看到的 conn 顺序 | closeAll 已经是"锁内快照 + 锁外迭代"，对顺序无依赖；本次测试用例覆盖并发 add/remove/closeAll |
| `Persister.LoadMany` 在 pgx 的 `ANY($1)` 不支持空数组场景下出错 | 调用前提前判断 `len(users) == 0` 直接返回空 map，避开 driver 边界 |
| Redis `MGET` / Postgres `ANY` 在 N 很大时单次请求过大 | `LoadMany` 实现必须内置 batch，起始 batch size 建议 512 或 1000，并用 benchmem/真实后端限制定型 |
| HTTP client 复用后，TLS root pool / NTP 时间函数变化无法生效 | `RootPoolFromContext` / `TimeFuncFromContext` 在 service 启动时一次性求值；当前没有运行期变更路径 |
| HTTP client lazy init 与 Close 并发导致 data race | 新增 `clientMu` 专门保护 `client` 字段与 `CloseIdleConnections`；`go test -race ./service/userprovider` 必须覆盖 |
| streaming JSON decoder 接受原来会被 `json.Unmarshal` 拒绝的 trailing garbage | Decode 主值后再 Decode 一次并要求 `io.EOF`，保持旧校验语义 |
| dirty-user 索引 drain 时丢失并发 `AddBytes` 事件 | 使用 `dirtyMu + dirtySet + dirtyQueue` 整体摘队；允许下一轮出现 zero-delta 空消费，但不得丢 delta |
| period reset 与 flush 并发导致旧周期 delta 写入新周期 | `AddBytes` / `ConsumePendingDeltas` / `CheckPeriodReset` 通过 `userState.periodAccess` 同步 `periodKey` 与 `pendingDelta` 快照 |
| period reset 删除旧周期后，迟到 flush 又写回旧周期记录 | `handlePeriodResets` 与 `flushPending` 的持久化 I/O 复用 `persistAccess` 串行化，确保 delete 与 flush 不交错复活旧记录 |
| 测试用 `runtime.NumGoroutine()` 差分受全局调度抖动影响 | 容忍 ±2；如仍 flaky，改为发送信号到一个测试-only 的"goroutine 已退出" channel |
| 修复后回归测试发现旧 `LoadAll` 调用方（如外部脚本）被移除路径不再触发 | `LoadAll` 接口暂不删除，仍可被外部测试/工具调用；本计划只切换 service.go 的调用点 |
| swap-and-zero / clear 实现错误导致 `closeAll` 看到 nil 元素后 panic | `closeAllAndClear` 采用锁内 snapshot + clear + `l.conns=nil`，锁外 mark；测试 TC1/TC3 直接断言无 nil 可见 |
| 用户担心 "`remove` 删错连接会断开正常用户连接" | Unit 3 的 Safety Proof P2/P3 已证明 `remove` 误操作的故障模式只能让 quota 失效（弱化），不可能让连接断开；R11 由此保障 |
| Unit 6 使用全局 lifecycle 写锁导致一个大用户 RemoveConfig 阻塞所有用户 RegisterConn | 写锁下只做 map 状态切换和 `LoadAndDelete`，`closeAllAndClear` 在锁外执行。**已通过 benchstat 对照验证（2026-05-26）**：post-fix 在 serial / parallel 路径上对比 NoLifecycle peer 均无回退（-11.45% / -16.53%，p < 0.05），未触发 striped lock 回退条件；详见 Unit 6 Verification 下的 Benchmark baseline 表 |
| admin-api timeout/body limit 破坏慢管理客户端或大批量 user create | timeout 取保守默认值，body limit 足够覆盖预期管理请求；新增测试覆盖边界大小和慢请求行为 |

## Documentation / Operational Notes

- 无需 migration：所有变更向后兼容，无配置字段增删，无 API surface 变化
- 无需额外监控指标；现有日志（`pushed N users to M inbound(s)`、`flush traffic quota usage`、`speed-limiter registered as connection tracker`）已覆盖
- 建议在 `docs/auth-features-README.md` 末尾的"容量建议"一节加：单实例稳态用户上限建议 ≤ 10 万；超过该规模需评估 `compatible.Map` / `connList` 的内存预算（每用户约 1KB 静态 + 每连接约 200B）

## Sources & References

- Related code:
  - `service/speedlimiter/manager.go:533-548,584-595`
  - `service/speedlimiter/service.go:65,117-121`
  - `service/userprovider/service.go:108-119,151-154`
  - `service/userprovider/source_http.go:80-116`
  - `service/userprovider/source_redis.go:75-115`
  - `service/userprovider/source_postgres.go:91-132`
  - `service/trafficquota/manager.go:64-99,171-187`
  - `service/trafficquota/service.go:351-411`
  - `service/trafficquota/persist.go`
  - `service/trafficquota/persist_redis.go:58-87`
  - `service/trafficquota/persist_postgres.go:65-86`
  - `service/trafficquota/conn.go:24-65`
  - `service/dynamicconfig/source_redis.go:67-91`
  - `service/dynamicconfig/source_postgres.go:81-103,156-174`
  - `common/compatible/map.go`
- Related tests:
  - `service/speedlimiter/service_test.go`
  - `service/userprovider/service_runtime_test.go`
  - `service/trafficquota/manager_test.go`
  - `service/trafficquota/persist_test.go`
  - `service/trafficquota/service_test.go`
- Related plans:
  - `docs/plans/2026-04-07-002-feat-traffic-quota-control-plan.md`
  - `docs/plans/2026-04-07-001-feat-per-user-speed-limiter-plan.md`
  - `docs/plans/2026-04-09-001-feat-per-client-speed-limiter-plan.md`
  - `docs/plans/2026-04-10-001-fix-user-provider-inbound-gaps-plan.md`
