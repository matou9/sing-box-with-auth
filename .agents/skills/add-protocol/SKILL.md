---
name: add-protocol
description: 添加新协议的完整流程。手动调用 /add-protocol <名称> 时使用。
invocation: user
---

# 添加新协议流程

## 接收参数
协议名称和规格说明。

## 六步实施流程

### Step 1: 添加类型常量
**文件**: `constant/proxy.go`

在常量块中添加新类型：
```go
const (
    // ... 现有常量 ...
    TypeYourProtocol = "yourprotocol"
)
```

并在 `ProxyDisplayName()` 函数中添加对应的 case。

### Step 2: 定义配置结构
**文件**: `option/yourprotocol.go`

Inbound 配置嵌入 `ListenOptions`：
```go
type YourProtocolInboundOptions struct {
    ListenOptions
    // TLS 支持（如需）
    InboundTLSOptionsContainer
    // V2Ray Transport 支持（如需）
    Transport *V2RayTransportOptions `json:"transport,omitempty"`
    // 协议特有字段
    Users []YourProtocolUser `json:"users,omitempty"`
}
```

Outbound 配置嵌入 `DialerOptions` + `ServerOptions`：
```go
type YourProtocolOutboundOptions struct {
    DialerOptions
    ServerOptions
    // TLS 支持（如需）
    OutboundTLSOptionsContainer
    // 协议特有字段
    Password string      `json:"password"`
    Network  NetworkList `json:"network,omitempty"`
}
```

### Step 3: 实现 Inbound
**文件**: `protocol/yourprotocol/inbound.go`

关键要素：
1. `RegisterInbound` 函数：`inbound.Register[option.YourProtocolInboundOptions](registry, C.TypeYourProtocol, NewInbound)`
2. 嵌入 `inbound.Adapter`（**不是** ~~adapter.InboundBase~~）
3. 构造函数签名：`func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.YourProtocolInboundOptions) (adapter.Inbound, error)`
4. `Start(stage)` 只在 `StartStateStart` 阶段执行
5. `Close()` 使用 `common.Close()` 关闭组件
6. `NewConnectionEx()` 处理连接，使用 `N.CloseOnHandshakeFailure` 处理错误

### Step 4: 实现 Outbound
**文件**: `protocol/yourprotocol/outbound.go`

关键要素：
1. `RegisterOutbound` 函数：`outbound.Register[option.YourProtocolOutboundOptions](registry, C.TypeYourProtocol, NewOutbound)`
2. 嵌入 `outbound.Adapter`（**不是** ~~adapter.OutboundBase~~）
3. 构造函数签名：`func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.YourProtocolOutboundOptions) (adapter.Outbound, error)`
4. 使用 `dialer.New(ctx, options.DialerOptions, options.ServerIsDomain())` 创建拨号器
5. 使用 `outbound.NewAdapterWithDialerOptions(...)` 初始化 Adapter
6. 实现 `DialContext()` 和 `ListenPacket()`

### Step 5: 注册接线
**文件**: `include/registry.go`

在 `InboundRegistry()` 函数中添加：
```go
yourprotocol.RegisterInbound(registry)
```

在 `OutboundRegistry()` 函数中添加：
```go
yourprotocol.RegisterOutbound(registry)
```

**如果需要构建标签**（可选编译），则在 `include/` 中创建对偶文件：
- `include/yourfeature.go` (//go:build with_yourfeature)
- `include/yourfeature_stub.go` (//go:build !with_yourfeature)

### Step 6: 编写测试
**文件**: `test/yourprotocol_test.go`

使用自测模式：同实例 Mixed inbound + 协议 inbound + 协议 outbound + 路由规则。

```go
const (
    serverPort uint16 = NNNNN + iota  // 选择未使用的基准端口
    clientPort
    testPort
)

func TestYourProtocolSelf(t *testing.T) {
    startInstance(t, option.Options{
        // ... Mixed inbound + 协议 inbound + 协议 outbound + 路由规则
    })
    testSuit(t, clientPort, testPort)
}
```

参考 `test/shadowsocks_test.go` 或 `test/trojan_test.go` 获取完整示例。

## 完成后验证

```bash
# 编译检查
make build

# 运行新测试
cd test && go test -v -tags "with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api" -run TestYourProtocol ./...

# 静态分析
make lint
```

## 常见错误避免
- **不要用** `adapter.OutboundBase` — 不存在，用 `outbound.Adapter`
- **不要用** side-effect import 注册 — 用泛型 `Register[T]()` 函数
- **不要忘记** 在 `constant/proxy.go` 添加类型常量
- **不要忘记** 在 `include/registry.go` 接线
- **不要忘记** 测试中初始化 `globalCtx = include.Context(context.Background())`
