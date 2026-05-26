---
name: protocol-dev
description: >
  sing-box 协议开发技能。提供添加新协议或修改现有协议的
  完整模板和检查清单。当涉及 protocol/ 目录修改时自动加载。
---

# 协议开发指南

## 新协议开发清单

### Step 1: 类型常量（constant/proxy.go）
```go
const (
    // 在现有常量列表中添加
    TypeYourProtocol = "yourprotocol"
)
```

### Step 2: 配置结构（option/yourprotocol.go）
```go
package option

// Inbound 配置（嵌入 ListenOptions）
type YourProtocolInboundOptions struct {
    ListenOptions
    // 如需 TLS：
    InboundTLSOptionsContainer
    // 如需 V2Ray Transport：
    Transport *V2RayTransportOptions    `json:"transport,omitempty"`
    // 协议特有字段
    Password string `json:"password,omitempty"`
}

// Outbound 配置（嵌入 DialerOptions + ServerOptions）
type YourProtocolOutboundOptions struct {
    DialerOptions
    ServerOptions
    // 如需 TLS：
    OutboundTLSOptionsContainer
    // 如需 V2Ray Transport：
    Transport *V2RayTransportOptions    `json:"transport,omitempty"`
    // 协议特有字段
    Password string                    `json:"password"`
    Network  NetworkList               `json:"network,omitempty"`
    Multiplex *OutboundMultiplexOptions `json:"multiplex,omitempty"`
}
```

### Step 3: Inbound 实现（protocol/yourprotocol/inbound.go）
```go
package yourprotocol

import (
    "context"
    "net"

    "github.com/sagernet/sing-box/adapter"
    "github.com/sagernet/sing-box/adapter/inbound"
    "github.com/sagernet/sing-box/common/listener"
    C "github.com/sagernet/sing-box/constant"
    "github.com/sagernet/sing-box/log"
    "github.com/sagernet/sing-box/option"
    "github.com/sagernet/sing/common"
    E "github.com/sagernet/sing/common/exceptions"
    N "github.com/sagernet/sing/common/network"
)

// 泛型注册函数
func RegisterInbound(registry *inbound.Registry) {
    inbound.Register[option.YourProtocolInboundOptions](registry, C.TypeYourProtocol, NewInbound)
}

// 编译时接口检查
var _ adapter.TCPInjectableInbound = (*Inbound)(nil)

type Inbound struct {
    inbound.Adapter                          // ✅ 正确的 base type
    router   adapter.ConnectionRouterEx
    logger   log.ContextLogger
    listener *listener.Listener
}

// 构造函数签名必须匹配注册要求
func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.YourProtocolInboundOptions) (adapter.Inbound, error) {
    inbound := &Inbound{
        Adapter: inbound.NewAdapter(C.TypeYourProtocol, tag),
        router:  router,
        logger:  logger,
    }
    inbound.listener = listener.New(listener.Options{
        Context:           ctx,
        Logger:            logger,
        Listen:            options.ListenOptions,
        ConnectionHandler: inbound,
    })
    return inbound, nil
}

// Start 只在 StartStateStart 阶段执行
func (h *Inbound) Start(stage adapter.StartStage) error {
    if stage != adapter.StartStateStart {
        return nil
    }
    return h.listener.Start()
}

// Close 用 common.Close() 关闭多个组件
func (h *Inbound) Close() error {
    return h.listener.Close()
}

// NewConnectionEx 处理新连接
func (h *Inbound) NewConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
    // 处理协议握手...
    metadata.Inbound = h.Tag()
    metadata.InboundType = h.Type()
    h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
    h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}
```

### Step 4: Outbound 实现（protocol/yourprotocol/outbound.go）
```go
package yourprotocol

import (
    "context"
    "net"

    "github.com/sagernet/sing-box/adapter"
    "github.com/sagernet/sing-box/adapter/outbound"
    "github.com/sagernet/sing-box/common/dialer"
    C "github.com/sagernet/sing-box/constant"
    "github.com/sagernet/sing-box/log"
    "github.com/sagernet/sing-box/option"
    "github.com/sagernet/sing/common/logger"
    M "github.com/sagernet/sing/common/metadata"
    N "github.com/sagernet/sing/common/network"
)

// 泛型注册函数
func RegisterOutbound(registry *outbound.Registry) {
    outbound.Register[option.YourProtocolOutboundOptions](registry, C.TypeYourProtocol, NewOutbound)
}

type Outbound struct {
    outbound.Adapter                         // ✅ 正确的 base type
    logger     logger.ContextLogger
    dialer     N.Dialer
    serverAddr M.Socksaddr
}

func NewOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.YourProtocolOutboundOptions) (adapter.Outbound, error) {
    outboundDialer, err := dialer.New(ctx, options.DialerOptions, options.ServerIsDomain())
    if err != nil {
        return nil, err
    }
    return &Outbound{
        Adapter:    outbound.NewAdapterWithDialerOptions(C.TypeYourProtocol, tag, options.Network.Build(), options.DialerOptions),
        logger:     logger,
        dialer:     outboundDialer,
        serverAddr: options.ServerOptions.Build(),
    }, nil
}

func (o *Outbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
    // 1. 建立底层连接
    conn, err := o.dialer.DialContext(ctx, N.NetworkTCP, o.serverAddr)
    if err != nil {
        return nil, err
    }
    // 2. 协议握手
    // 3. 返回包装后的连接
    return conn, nil
}

func (o *Outbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
    // UDP 支持（如果需要）
    return nil, os.ErrInvalid
}
```

### Step 5: 注册接线（include/registry.go）
```go
// 在 InboundRegistry() 中添加：
yourprotocol.RegisterInbound(registry)

// 在 OutboundRegistry() 中添加：
yourprotocol.RegisterOutbound(registry)
```

**注意：** 不要使用 side-effect import (`import _`)，直接调用注册函数。

### Step 6: 测试（test/yourprotocol_test.go）
```go
func TestYourProtocolSelf(t *testing.T) {
    startInstance(t, option.Options{
        Inbounds: []option.Inbound{
            {
                Type: C.TypeMixed,
                Tag:  "mixed-in",
                Options: &option.HTTPMixedInboundOptions{
                    ListenOptions: option.ListenOptions{
                        Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
                        ListenPort: clientPort,
                    },
                },
            },
            {
                Type: C.TypeYourProtocol,
                Options: &option.YourProtocolInboundOptions{
                    ListenOptions: option.ListenOptions{
                        Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
                        ListenPort: serverPort,
                    },
                    Password: "test-password",
                },
            },
        },
        Outbounds: []option.Outbound{
            {Type: C.TypeDirect},
            {
                Type: C.TypeYourProtocol,
                Tag:  "your-out",
                Options: &option.YourProtocolOutboundOptions{
                    ServerOptions: option.ServerOptions{
                        Server:     "127.0.0.1",
                        ServerPort: serverPort,
                    },
                    Password: "test-password",
                },
            },
        },
        Route: &option.RouteOptions{
            Rules: []option.Rule{
                {
                    Type: C.RuleTypeDefault,
                    DefaultOptions: option.DefaultRule{
                        RawDefaultRule: option.RawDefaultRule{
                            Inbound: []string{"mixed-in"},
                        },
                        RuleAction: option.RuleAction{
                            Action: C.RuleActionTypeRoute,
                            RouteOptions: option.RouteActionOptions{
                                Outbound: "your-out",
                            },
                        },
                    },
                },
            },
        },
    })
    testSuit(t, clientPort, testPort)
}
```

## 修改现有协议检查清单

- [ ] 理解原有实现（读 protocol/<name>/ 所有文件）
- [ ] 确认 adapter 接口是否需要变更
- [ ] 修改 protocol/<name>/ 代码
- [ ] 同步 option/<name>.go 配置变更
- [ ] 检查 include/registry.go 注册是否需要更新
- [ ] 运行 `make build` 验证编译
- [ ] 运行相关测试 `cd test && go test -v -tags "..." -run TestName`
- [ ] 检查是否影响其他协议
- [ ] 更新 docs/ 文档（如果有用户可见变更）
