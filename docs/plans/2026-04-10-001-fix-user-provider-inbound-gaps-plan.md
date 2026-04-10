---
title: "fix: Close user-provider gaps for shadowsocks / naive / shadowtls inbounds"
type: fix
status: active
date: 2026-04-10
---

# fix: Close user-provider gaps for shadowsocks / naive / shadowtls inbounds

## Overview

三类入站在接入 `user_provider` 时仍会启动失败：

1. `shadowsocks` 入站：当未内联 `users` 时，`NewInbound` 分支到单用户 `Inbound`，不实现 `adapter.ManagedUserServer`，user-provider 启动阶段抛出 `inbound/shadowsocks[ss-in] does not support user-provider`。
2. `naive` 入站：`NewInbound` 强制校验 `len(options.Users) > 0`，未内联 users 时直接返回 `missing users`，配置阶段就失败。
3. `shadowtls` 入站：底层库 `sing-shadowtls.NewService` 在 `version == 3` 时强制要求 `len(users) > 0`，未内联 users 时配置阶段就失败。

本计划让这三类入站可以"空 users 启动 + user-provider 后续下发"，且不破坏任何已有内联 users 配置。

## Problem Frame

项目已实现 user-provider 统一用户管理（`service/userprovider`）。大部分入站（trojan/vmess/vless/hysteria2/tuic/...）都已支持"空 users 入站 + user-provider Start 时 ReplaceUsers 推送"的模式。但 shadowsocks / naive / shadowtls 因为历史代码路径或底层库约束，没有走这一路径，导致用户一旦不再内联 users 就启动失败。

用户希望的心智模型是：只要入站被 user-provider 引用，就不再需要内联 `users` 字段。

## Requirements Trace

- R1. `shadowsocks` 入站在未内联 users 的情况下能被 user-provider 托管，支持 `shadowaead` 和 `shadowaead_2022` 系列方法
- R2. `naive` 入站在未内联 users 的情况下允许启动，并在 user-provider 推送用户后正常鉴权
- R3. `shadowtls` 入站（含 version 3）在未内联 users 的情况下允许启动，并在 user-provider 推送用户后正常鉴权
- R4. 保持已有内联 `users` 配置完全向后兼容（行为不变）
- R5. 非 user-provider 场景下的行为不得回退：未内联 users、未被 user-provider 引用的单用户 shadowsocks 仍按原有单用户模式工作
- R6. 启动期间（inbound.Start 之后、user-provider.Start 之前）到达的连接应被拒绝而不是被未初始化的服务 panic
- R7. 补齐自动化测试，三类入站都要覆盖 "空 users + user-provider 下发" 的端到端流程

## Scope Boundaries

- 不修改 `sing-shadowtls` 上游库，所有变动在 sing-box 侧完成
- 不改变 shadowsocks 2022 主 PSK 的语义（`password` 仍为主 PSK）
- 不引入新的顶层配置项（复用现有 `managed` 字段与 user-provider 引用关系）
- 不重构 box.go 整体启动顺序，只在现有 inbound 创建前插入一个只读的预扫描步骤
- 不处理 `shadowsocks` 的 `destinations`（relay）模式，该模式与 user-provider 无关

## Context & Research

### Relevant Code and Patterns

- `service/userprovider/service.go:83-94` — 类型断言 `inbound.(adapter.ManagedUserServer)`，失败则抛错
- `adapter/user_provider.go:14-19` — `ManagedUserServer` 接口定义（`Inbound + ReplaceUsers([]User) error`）
- `protocol/shadowsocks/inbound.go:28-45` — `RegisterInbound` / `NewInbound` 三分支：`newMultiInbound` / `newRelayInbound` / `newInbound`
- `protocol/shadowsocks/inbound.go:47` — 单 `Inbound` 只声明 `TCPInjectableInbound`，未实现 `ManagedUserServer`
- `protocol/shadowsocks/inbound_multi.go:31-35` — `MultiInbound` 实现 `ManagedUserServer`、`ManagedSSMServer`
- `protocol/shadowsocks/inbound_multi.go:86-96` — 初始化时 `if len(options.Users) > 0` 才调用 `UpdateUsersWithPasswords`，天然兼容空 users
- `protocol/shadowsocks/inbound_multi.go:126-149` — `ReplaceUsers` / `UpdateUsers` 已实现
- `option/shadowsocks.go:3-12` — `ShadowsocksInboundOptions` 已有 `Managed bool` 字段（存量能力）
- `protocol/naive/inbound.go:70` — `authenticator: auth.NewAuthenticator(options.Users)`（空切片也合法）
- `protocol/naive/inbound.go:77-79` — `if len(options.Users) == 0 { return nil, E.New("missing users") }`（本次要拆除的硬校验）
- `protocol/naive/inbound.go:151-161` — `ReplaceUsers` 已实现，直接重建 `authenticator`
- `protocol/naive/inbound.go:174-182` — HTTP 请求鉴权走 `n.authenticator.Verify`，空用户集会直接拒绝
- `protocol/shadowtls/inbound.go:38-102` — `NewInbound` 立即调用 `shadowtls.NewService(inbound.serviceConfig)`
- `protocol/shadowtls/inbound.go:115-130` — `ReplaceUsers` 复制 `serviceConfig`、重建 `*shadowtls.Service`
- `sing-shadowtls@v0.2.1-...service.go:85-92` — version 3 强制 `len(users) > 0`，否则 `missing users`
- `box.go:267-303` — inbounds 在 services 之前构造；services 在 inbounds 之后构造
- `box.go:286-303` — `for i, serviceOptions := range options.Services` 按顺序注入
- `service/userprovider/service.go:79-121` — `Start` 按顺序解析 inbound tag、调用 `loadAndPush` → `ReplaceUsers`
- `test/user_provider_test.go` — 已有 trojan/vless/vmess 的 user-provider 端到端测试可照抄

### Institutional Learnings

- 单 `Inbound` 与 `MultiInbound` 的关键差异：单版本调用 `shadowaead[_2022].NewService(...)`，多版本调用 `NewMultiService[int](...)`。`password` 字段在 2022 方法下是主 PSK、在 legacy AEAD 下是直连密钥 —— 对 legacy AEAD，强行切到 multi 会丢掉主 PSK 的认证能力（多模式依赖 uPSK）。因此不能无条件切换，而是只在"用户期望 managed"时才切。
- Box 启动顺序 `Inbound → Service`，决定了 inbound 初始化时必须能在"当时 users 为空"的前提下保证后续 ReplaceUsers 生效。已有的 trojan/vmess/vless 都遵循同一做法。

### External References

- `sing-shadowtls@v0.2.1-0.20250503051639-fcd445d33c11/service.go` — 只读引用，确认 version 1/2 不检查 users，version 3 强制检查

## Key Technical Decisions

- **`shadowsocks` 使用 box.go 预扫描把被 user-provider 引用的 shadowsocks 入站自动置为 `Managed = true`**
  - 在 `box.go:267` 之前插入 `collectUserProviderManagedInbounds(options.Services)`，返回一个 `map[string]struct{}`（inbound tag 集合）
  - 在 `for i, inboundOptions := range options.Inbounds` 循环内，对 `Type == C.TypeShadowsocks` 且 tag 命中集合的条目，将 `options.Options.(*option.ShadowsocksInboundOptions).Managed` 置为 `true`（配合 `len(Destinations) == 0`、`len(Users) == 0` 的守卫）
  - 之后 `shadowsocks.NewInbound` 走已有的 `options.Managed` 分支 → `newMultiInbound`，自动获得 `ManagedUserServer` 能力
  - **为什么不自动切到 MultiInbound**：legacy AEAD 下"内联 password 单用户"和"多用户"是两种互斥语义，无条件切换会悄悄破坏现有部署。预扫描的做法只在"被 user-provider 显式引用"时才切，零误伤
  - **为什么不新增接口**：`Managed` 字段已存在，零侵入
- **`naive` 直接移除 `missing users` 硬校验**
  - `auth.NewAuthenticator(nil)` 是合法的（空白名单，默认拒绝所有请求）
  - 在 user-provider 引用该入站时，`Start` 期间会立即 `ReplaceUsers` 覆盖
  - 如果用户既没有内联 users、也没有 user-provider 引用，会在请求时被 `authenticator.Verify` 拒绝 —— 从"启动失败"退化为"运行时全部 401"，行为更符合直觉
  - **不引入"必须被 user-provider 引用"的校验**：user-provider 与 inbound 都在 `box.New` 内构造，顺序上 inbound 先、service 后，inbound 初始化时无法知道是否会被引用；强行交叉校验会让 box.go 复杂度爆炸
- **`shadowtls` 采用"延迟创建 `*shadowtls.Service`"策略**
  - `NewInbound` 时保留 `serviceConfig`，但当 `Version == 3 && len(Users) == 0` 时**不立即**调用 `shadowtls.NewService`
  - 将 `inbound.service` 留为 `nil`，并在 `Start` 阶段进行自我检查：若 `service == nil` 则**不启动 listener**直接返回错误 —— 但这会阻断 user-provider 流程
  - **改进做法**：`NewInbound` 构造成功（service = nil），`Start` 照常启动 listener，`NewConnectionEx` 中遇 `service == nil` 时直接返回 `E.New("shadowtls service not ready: waiting for user-provider users")`（连接被关闭但 listener 保持可用），等 `ReplaceUsers` 被调用时会重建 service
  - **为什么不用占位 user 欺骗上游库**：placeholder user 需要在 ReplaceUsers 时被严格清理，一旦 user-provider 推送失败或下发空列表，placeholder 会变成一个真实可用的后门账户。延迟创建没有这种风险
  - **并发安全**：`h.service` 读写加一把 `sync.RWMutex`（只在 `ReplaceUsers` / `NewConnectionEx` 访问；冷热路径都很轻），与 trojan/vmess 的做法一致
- **文档同步**：`docs/auth-features-README.md` 已列出支持 `ManagedUserServer` 的 inbound，本次修复后需要明确写出"shadowsocks 需被 user-provider 引用即可，无需手动 `managed: true`"的行为

## Open Questions

### Resolved During Planning

- **Q: shadowsocks legacy AEAD (`aes-256-gcm` 等) 单用户配置能否也走 user-provider？**
  → 可以，但用户必须理解：legacy AEAD 多用户模式会忽略 `password` 主密钥，完全以 user-provider 下发的 per-user 密钥为准。预扫描路径自动启用 `Managed` 后，legacy AEAD 单用户 + user-provider 的组合等价于多用户模式下每个用户独立 key。`password` 字段在此场景下变为可选。
- **Q: 为什么不在 user-provider 的 `Start` 阶段把非 managed 的 shadowsocks 单用户 Inbound 升级？**
  → Start 阶段入站已经监听了端口、`shadowaead.Service` 已经构造，热切换会丢失连接并引入竞态。预扫描在入站创建之前完成，是唯一无状态切换时机。
- **Q: 如果用户同时内联 `users` 又配置 user-provider 引用？**
  → 已在 MultiInbound 中处理：`NewInbound` 构造时用内联 users 初始化服务，user-provider Start 时 `ReplaceUsers` 覆盖（与 trojan/vmess 行为一致）。预扫描仅对未内联 users 的入站生效。
- **Q: shadowtls 的 NewConnectionEx 在 service==nil 时返回 error，会不会被上层当成崩溃？**
  → 该路径走 `N.CloseOnHandshakeFailure(conn, onClose, err)`，属于正常的握手失败，和其他协议早期鉴权失败一致；logger 会记录 warn 级别。

### Deferred to Implementation

- `collectUserProviderManagedInbounds` 的确切位置（box.go 顶部独立函数 vs. `include/registry.go` 辅助函数）和重复 tag 合并策略，最终落位等看 box.go 上下文后决定
- shadowtls `sync.RWMutex` vs `atomic.Pointer[shadowtls.Service]` 的取舍：两者皆可，最终选择以实现时对代码简洁度的判断为准，不影响接口
- 单元测试是否为 box.go 预扫描单独建立 package 级测试，或仅靠 test/ 集成测试覆盖
- 对 `shadowsocks.NewInbound` 是否同步补一条守卫日志："detected user-provider management, upgrading to multi mode"

## High-Level Technical Design

> *This illustrates the intended approach and is directional guidance for review, not implementation specification. The implementing agent should treat it as context, not code to reproduce.*

### box.go 预扫描与 shadowsocks 升级

```text
box.New(options):
  ...
  managedInboundTags := collectUserProviderManagedInbounds(options.Services)
  // 遍历 options.Services，仅认 type == "user_provider" 条目
  // 把每个 entry 的 Inbounds []string 收集进集合

  for i, inboundOptions := range options.Inbounds:
      if inboundOptions.Type == TypeShadowsocks:
          tag := tagOf(inboundOptions, i)
          if _, managed := managedInboundTags[tag]; managed:
              ssOpts := inboundOptions.Options.(*option.ShadowsocksInboundOptions)
              if len(ssOpts.Destinations) == 0 && len(ssOpts.Users) == 0:
                  ssOpts.Managed = true
      inboundManager.Create(...)
  ...
```

### shadowtls 延迟创建

```text
NewInbound:
  inbound.serviceConfig = <build config>
  if version == 3 && len(users) == 0:
      // defer: inbound.service = nil, wait for ReplaceUsers
  else:
      inbound.service = shadowtls.NewService(config)

NewConnectionEx:
  svc := inbound.loadService()  // atomic/RWMutex
  if svc == nil:
      closeConn with "shadowtls service not ready"
      return
  svc.NewConnection(...)

ReplaceUsers(users):
  config := inbound.serviceConfig
  config.Users = map(users)
  svc := shadowtls.NewService(config)
  inbound.storeService(svc)   // atomic/RWMutex
```

## Implementation Units

- [ ] **Unit 1: box.go 预扫描 user-provider 引用的 shadowsocks 入站**

**Goal:** 在 inbound 创建之前，自动把被 user-provider 引用的 shadowsocks 入站升级为 `Managed = true`

**Requirements:** R1, R4, R5

**Dependencies:** 无

**Files:**
- Modify: `box.go`
- Test: `test/user_provider_shadowsocks_test.go` (new)

**Approach:**
- 新增私有函数 `collectUserProviderManagedInbounds(services []option.Service) map[string]struct{}`，仅识别 `Type == C.TypeUserProvider`
- 在 `for i, inboundOptions := range options.Inbounds` 循环体最前面插入升级逻辑：仅当 `Type == C.TypeShadowsocks`、`tag` 命中集合、`Destinations` 为空、`Users` 为空时，把 `*option.ShadowsocksInboundOptions.Managed` 置为 `true`
- tag 解析沿用同一块代码中 `if inboundOptions.Tag != ""` 的既有逻辑
- 保持对其他 inbound 类型零影响

**Patterns to follow:**
- `box.go:267-285` 的 tag 解析与 `inboundManager.Create` 调用

**Test scenarios:**
- Happy path: shadowsocks 入站未内联 users、user-provider 引用该 tag → 启动成功、`testSuit` 能通过 2022 方法建链
- Happy path: shadowsocks 入站未内联 users、user-provider 未引用 → 仍按单用户路径启动，原 `password` 仍可用
- Edge case: shadowsocks 入站内联了 `users: [...]` + user-provider 引用 → 预扫描不动 `Managed`，保留原多用户路径；user-provider Start 后 ReplaceUsers 覆盖
- Edge case: 同一 tag 被多个 user_provider 引用 → 只需在集合中出现即可，不重复升级
- Edge case: shadowsocks 入站 `Destinations` 非空（relay 模式）→ 预扫描跳过，保留 relay 分支
- Integration: 启动 box、mixed-in → ss-in → outbound，通过 user-provider 下发 sekai 用户，验证 2022 和 `aes-256-gcm` 两种方法都能通

**Verification:**
- `make build` 通过
- 新测试 `TestUserProviderShadowsocks2022` / `TestUserProviderShadowsocksLegacy` 通过
- 原有 shadowsocks 内联 users 测试不回归

- [ ] **Unit 2: naive 入站移除 `missing users` 硬校验**

**Goal:** 允许 `naive` 入站在未内联 users 的情况下被 user-provider 托管

**Requirements:** R2, R4, R6

**Dependencies:** 无

**Files:**
- Modify: `protocol/naive/inbound.go`
- Test: `test/user_provider_naive_test.go` (new)

**Approach:**
- 删除 `protocol/naive/inbound.go:77-79` 的 `len(options.Users) == 0` 校验
- 保留 `authenticator: auth.NewAuthenticator(options.Users)`（已能处理空切片）
- 不改动 `ReplaceUsers`、不改动 `ServeHTTP` 的鉴权路径

**Patterns to follow:**
- `protocol/trojan/inbound.go:93-100` — 空 users 时直接 `UpdateUsers([], [])`，无硬校验
- `protocol/vmess/inbound.go:74-83` — 相同模式

**Test scenarios:**
- Happy path: naive 入站未内联 users、user-provider 引用 → 启动成功、sekai 用户能通过 Proxy-Authorization 基本鉴权连接
- Error path: naive 入站未内联 users、未被 user-provider 引用、收到带账号密码的请求 → HTTP 407 (`authenticator.Verify` 返回 false)
- Error path: naive 入站未内联 users、未被 user-provider 引用、收到无 Authorization 的请求 → HTTP 407
- Edge case: naive 入站内联了 users + user-provider 引用 → user-provider 下发后覆盖，下发前仍可用内联凭证

**Verification:**
- `make build` 通过
- 新测试通过
- 原 naive 内联 users 测试不回归

- [ ] **Unit 3: shadowtls 入站支持延迟创建 service**

**Goal:** `shadowtls` v3 在未内联 users 的情况下允许启动，service 在 `ReplaceUsers` 时才被构造

**Requirements:** R3, R4, R6

**Dependencies:** 无

**Files:**
- Modify: `protocol/shadowtls/inbound.go`
- Test: `test/user_provider_shadowtls_test.go` (new)

**Approach:**
- `NewInbound`：当 `options.Version == 3 && len(options.Users) == 0`，**跳过** `shadowtls.NewService(inbound.serviceConfig)`，保留 `inbound.service = nil`
- 其余分支（v1/v2，或 v3+内联 users）维持原样立即构造 service
- 引入 `sync.RWMutex`（或 `atomic.Pointer[shadowtls.Service]`）保护 `h.service` 读写
- `NewConnectionEx`：加载当前 service；若为 nil，记录 warn 日志并返回 `E.New("shadowtls service not ready: user-provider users not yet loaded")`，走 `N.CloseOnHandshakeFailure` 正常关闭
- `ReplaceUsers`：复用现有重建逻辑，把新 service 写回 `h.service`
- `Close`：清空 service 引用以协助 GC（可选）

**Patterns to follow:**
- `protocol/shadowtls/inbound.go:115-130` 现有 `ReplaceUsers` 的 service 重建流程
- `protocol/trojan/inbound.go` 中 `service *trojan.Service[int]` 字段的读写模式

**Test scenarios:**
- Happy path: shadowtls v3 未内联 users、user-provider 引用 → 启动成功、sekai 用户握手成功
- Happy path: shadowtls v1 / v2 未内联 users → 与既有行为一致（上游库不检查 users）
- Edge case: shadowtls v3 内联 users → 与既有行为一致，service 立即构造
- Error path: shadowtls v3 未内联 users、未被 user-provider 引用 → listener 启动，连接到达时 `NewConnectionEx` 返回 "service not ready" 错误并关闭连接，不 panic
- Edge case: ReplaceUsers 期间有并发连接 → RWMutex 保护 `h.service` 的读写，读协程看到的是旧 service 或新 service，不得看到中间态
- Integration: 启动 box、mixed-in → st-in → outbound，验证 v3 + user-provider 的端到端流量

**Verification:**
- `make build` 通过
- 新测试通过
- 原 shadowtls 内联 users 测试不回归

- [ ] **Unit 4: 补齐 user-provider 集成测试与文档**

**Goal:** 端到端验证三类入站的"空 users + user-provider"场景，并同步文档

**Requirements:** R7

**Dependencies:** Unit 1, Unit 2, Unit 3

**Files:**
- Create: `test/user_provider_shadowsocks_test.go`
- Create: `test/user_provider_naive_test.go`
- Create: `test/user_provider_shadowtls_test.go`
- Modify: `docs/auth-features-README.md`
- Modify: `test/user_provider_test.go` (扩充 `TestUserProviderManagedUserServerInterface` 的断言)

**Approach:**
- 仿照 `test/user_provider_test.go` 中 `TestUserProviderTrojanVLESS` 的骨架：同一 box 内 `mixed-in → ss-in|naive-in|st-in → direct`，Services 中声明 `user_provider` 引用对应 tag 并内联 `sekai` 用户
- Shadowsocks 测试矩阵：`2022-blake3-aes-128-gcm` + `aes-256-gcm` 各一条，显式不内联 `users`
- Naive 测试：TCP 模式即可，附带自签 TLS（仿照 `createSelfSignedCertificate` 辅助）
- ShadowTLS 测试：version 3 + handshake 指向 google.com（沿用已有 shadowtls 测试中的 mock handshake 模式；若现有测试用 `127.0.0.1` mock，继续沿用）
- 扩充 `TestUserProviderManagedUserServerInterface`：显式断言 `(*shadowsocks.MultiInbound)`、`(*naive.Inbound)`、`(*shadowtls.Inbound)` 都能被断言为 `adapter.ManagedUserServer`
- 文档更新：在 `docs/auth-features-README.md` 中支持的 inbound 列表旁标注 "shadowsocks 需无 inline `users` 即可自动转为 managed 多用户模式"

**Patterns to follow:**
- `test/user_provider_test.go:TestUserProviderTrojanVLESS`
- `test/box_test.go` 的 `startInstance` / `testSuit` 用法

**Test scenarios:**
- Happy path: 三类入站各一条端到端测试，使用 `testSuit` 验证 TCP/UDP 连通（naive/shadowtls 仅 TCP）
- Integration: `TestUserProviderManagedUserServerInterface` 增加 shadowsocks/naive/shadowtls 的类型断言检查
- Integration: 对 shadowsocks 场景额外断言：启动时 `ss-in` 的 `options.Managed` 被自动置为 true（通过单独的 unit-level 测试覆盖 `collectUserProviderManagedInbounds`）

**Verification:**
- `cd test && go test -v -tags "..." -run "TestUserProviderShadowsocks|TestUserProviderNaive|TestUserProviderShadowTLS|TestUserProviderManagedUserServerInterface" ./...` 全部通过
- 文档变更符合既有中文风格与章节层级

## System-Wide Impact

- **Interaction graph:** `box.New` 的预扫描只读 `options.Services`，对其他 inbound 类型零影响；对 shadowsocks 仅在 "被引用 + 无内联 users + 无 destinations" 三条件同时满足时修改一个 bool 字段。user-provider → inbound 的 ReplaceUsers 调用链不变。
- **Error propagation:** naive 由"启动期 fatal"退化为"运行期 HTTP 407"；shadowtls 由"启动期 fatal"退化为"连接级 handshake failure"。两者都比原先更优雅，不会阻断整个 box 启动。
- **State lifecycle risks:** shadowtls `service` 字段的并发读写需要同步原语；从启动到首次 ReplaceUsers 之间存在"service 为 nil"的窗口期，本计划显式地让该窗口内的所有连接失败，避免了 nil 解引用。
- **API surface parity:** 其他已经支持 user-provider 的 inbound（trojan/vmess/vless/hysteria2/tuic/socks/http/mixed/hysteria/anytls）的语义不变。本次修复让 shadowsocks/naive/shadowtls 达到同等行为。
- **Integration coverage:** Unit 4 的端到端测试保证 box.New → inbound.Start → user-provider.Start → ReplaceUsers → 真实流量 这一完整链路都能跑通，单测无法验证这里的时序交互。
- **Unchanged invariants:**
  - shadowsocks 内联 `users` 的单用户语义（legacy AEAD 下 `password` 为直连密钥）不变
  - naive 内联 `users` + TLS + HTTP/2 鉴权路径不变
  - shadowtls v1/v2 路径、v3 + 内联 users 路径完全不变
  - `adapter.ManagedUserServer` 接口定义不变
  - user-provider 的文件/HTTP/Redis/Postgres 数据源不变

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| box.go 预扫描误判某个 shadowsocks tag 并在用户不希望 managed 时改写 `Managed = true` | 三重守卫：tag 必须命中 user-provider 引用集合 + `Destinations` 为空 + `Users` 为空；任何一条不满足都跳过。新增单元测试覆盖每条 guard |
| shadowtls 的 nil service 窗口在 user-provider Start 之前接收到真实流量导致用户困惑 | `NewConnectionEx` 返回 `service not ready` 并 warn 日志；文档提示 user-provider 与 shadowtls 入站处于同一 box 时该窗口通常只有毫秒级 |
| legacy AEAD shadowsocks 用户原本依赖 `password` 字段做单用户鉴权，误配成 user-provider 引用后会丢掉原 `password` 的语义 | 在 `docs/auth-features-README.md` 的 shadowsocks 段落明确说明"一旦被 user-provider 引用，`password` 字段仅在 2022 方法下作为主 PSK 有效；legacy AEAD 将完全以下发的 per-user 密钥为准" |
| shadowtls `sync.RWMutex` 与上游库内部的并发模型冲突 | 该锁仅保护 sing-box 侧的 service 指针读写，不进入上游库；上游库内部自有并发控制 |
| 新增 test 文件在 Windows / Linux 不同行为（端口冲突、自签证书路径） | 沿用 `test/box_test.go` 现有辅助，与既有 trojan/vless 测试同构，自动跨平台 |

## Documentation / Operational Notes

- `docs/auth-features-README.md` 需要新增一节说明"shadowsocks 自动 managed 升级"和"naive/shadowtls 空 users 行为"
- 无需 migration：所有变更向后兼容，无配置字段增删
- 无需额外监控/告警；现有 user-provider 推送日志 (`pushed N users to M inbound(s)`) 已覆盖可观测性

## Sources & References

- Related code:
  - `box.go:267-303`
  - `protocol/shadowsocks/inbound.go`
  - `protocol/shadowsocks/inbound_multi.go`
  - `protocol/naive/inbound.go`
  - `protocol/shadowtls/inbound.go`
  - `service/userprovider/service.go`
  - `adapter/user_provider.go`
  - `option/shadowsocks.go`
- Related tests:
  - `test/user_provider_test.go`
- External docs:
  - `sing-shadowtls@v0.2.1-0.20250503051639-fcd445d33c11/service.go`
