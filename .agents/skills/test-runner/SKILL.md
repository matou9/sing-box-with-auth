---
name: test-runner
description: >
  sing-box 测试执行策略。根据修改范围智能选择需要运行的测试，
  避免全量测试的时间开销。
---

# 测试执行策略

## 核心测试辅助函数

### startInstance — 启动 sing-box 实例
```go
// test/box_test.go
func startInstance(t *testing.T, options option.Options) *box.Box
```
- 自动设置日志级别（debug 模式下用 trace，否则 warning）
- 重试 3 次启动（间隔 1 秒）
- 自动注册 t.Cleanup 关闭实例

### testSuit — 运行标准连通性测试
```go
// test/box_test.go
func testSuit(t *testing.T, clientPort uint16, testPort uint16)
```
- 通过 SOCKS5 代理连接测试 TCP/UDP
- 测试 ping-pong、大数据传输

### 全局上下文初始化
```go
var globalCtx context.Context

func init() {
    globalCtx = include.Context(context.Background())
}
```
**必须**在测试文件中初始化，否则无法创建任何协议实例。

### 端口常量 iota 模式
```go
const (
    serverPort uint16 = 10000 + iota  // 每个测试文件用不同的基准端口
    clientPort
    testPort
    otherPort
)
```

## 自测模式（标准测试结构）

使用同一个实例配置 Mixed inbound（客户端入口）+ 协议 inbound（服务端）+ 协议 outbound（客户端）+ 路由规则：

```go
startInstance(t, option.Options{
    Inbounds: []option.Inbound{
        {
            Type: C.TypeMixed, Tag: "mixed-in",
            Options: &option.HTTPMixedInboundOptions{
                ListenOptions: option.ListenOptions{
                    Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
                    ListenPort: clientPort,
                },
            },
        },
        {
            Type: C.TypeXxx,  // 被测协议 inbound
            Options: &option.XxxInboundOptions{
                ListenOptions: option.ListenOptions{
                    Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
                    ListenPort: serverPort,
                },
                // 协议配置...
            },
        },
    },
    Outbounds: []option.Outbound{
        {Type: C.TypeDirect},
        {
            Type: C.TypeXxx, Tag: "xxx-out",
            Options: &option.XxxOutboundOptions{
                ServerOptions: option.ServerOptions{
                    Server: "127.0.0.1", ServerPort: serverPort,
                },
                // 协议配置...
            },
        },
    },
    Route: &option.RouteOptions{
        Rules: []option.Rule{{
            Type: C.RuleTypeDefault,
            DefaultOptions: option.DefaultRule{
                RawDefaultRule: option.RawDefaultRule{Inbound: []string{"mixed-in"}},
                RuleAction: option.RuleAction{
                    Action:       C.RuleActionTypeRoute,
                    RouteOptions: option.RouteActionOptions{Outbound: "xxx-out"},
                },
            },
        }},
    },
})
testSuit(t, clientPort, testPort)
```

## 按修改范围选择测试

### 修改 protocol/<name>/
```bash
# 运行该协议的集成测试（test/ 目录有自己的 go.mod）
cd test && go test -v -tags "with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api" -run "Test<Name>" ./...
```

### 修改 route/ 或 dns/
```bash
# 路由/DNS 影响面广，建议全量
cd test && go test -v -tags "..." ./...
```

### 修改 option/
```bash
# 编译检查 + 相关协议测试
go build -tags "..." ./...
cd test && go test -v -tags "..." -run "Test<相关协议>" ./...
```

### 修改 common/
```bash
# common 是基础库，全量测试 + 竞态检测
cd test && go test -race -tags "..." ./...
```

## 快速验证（每次修改后）
```bash
# 编译检查（最快反馈）
go build -tags "with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_tailscale,with_ccm,badlinkname,tfogo_checklinkname0" ./...
```

## 完整验证（提交前）
```bash
make build    # 使用 Makefile 定义的完整 tags
make test     # 运行测试
make lint     # 静态分析
```

## 调试测试
```bash
# 启用 trace 日志
cd test && go test -v -tags "..." -run TestName -count=1
# 测试内部使用 debug.Enabled 控制日志级别
```
