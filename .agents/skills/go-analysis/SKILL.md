---
name: go-analysis
description: >
  Go 代码深度分析技能。用于理解 Go 代码结构、接口实现关系、
  依赖图、构建标签影响。当需要分析 Go 代码时自动加载。
---

# Go 代码分析指南

## 快速定位方法

### 查找接口实现
```bash
# 找到实现某接口的所有类型（使用编译时断言）
grep -rn "var _ adapter\." protocol/ --include="*.go"
# 示例结果：
# var _ adapter.TCPInjectableInbound = (*Inbound)(nil)
# var _ adapter.Outbound = (*Outbound)(nil)
```

### 追踪泛型注册
```bash
# 找所有 Inbound 注册
grep -rn "inbound.Register\[" protocol/ include/ --include="*.go"
# 找所有 Outbound 注册
grep -rn "outbound.Register\[" protocol/ include/ --include="*.go"
# 找所有 DNS Transport 注册
grep -rn "dns.RegisterTransport\[" dns/ include/ --include="*.go"
# 找所有 Service 注册
grep -rn "service.Register\[" service/ include/ --include="*.go"
```

### 服务上下文查找
```bash
# 找所有 service.FromContext 使用
grep -rn "service.FromContext\[" --include="*.go" .
# 常见模式：
# router := service.FromContext[adapter.Router](ctx)
# outbound := service.FromContext[adapter.OutboundManager](ctx)
# network := service.FromContext[adapter.NetworkManager](ctx)

# 找所有 service.MustRegister 使用
grep -rn "service.MustRegister\[" --include="*.go" .
```

### 追踪类型定义和使用
```bash
# 找定义
grep -rn "type OutboundOptions struct" option/
# 找使用
grep -rn "OutboundOptions" --include="*.go" .
```

### 分析构建标签
```bash
# 列出所有构建标签
grep -rn "//go:build" --include="*.go" . | sort -u
# 查看某个标签涉及的文件
grep -rl "//go:build.*with_quic" --include="*.go" .
```

### 查看包依赖
```bash
go list -m all                    # 所有外部依赖
go mod graph | grep "sing"        # sing 相关依赖
go list -deps ./protocol/trojan/  # trojan 包的所有依赖
```

## 常见分析模式

### 模式 1：追踪一个网络请求的完整路径
1. **入口**: `protocol/<name>/inbound.go` → `NewConnectionEx(ctx, conn, metadata, onClose)`
2. **设置元数据**: `metadata.Inbound = h.Tag()`, `metadata.InboundType = h.Type()`
3. **路由**: `router.RouteConnectionEx(ctx, conn, metadata, onClose)`
4. **规则匹配**: `route/rule/` 中的 `DefaultRule.Match()`
5. **出站**: `protocol/<name>/outbound.go` → `DialContext(ctx, network, destination)`
6. **连接管理**: `route/conn.go` → `ConnectionManager.NewConnection()`
7. **底层拨号**: `common/dialer/` → 实际网络连接

### 模式 2：理解一个配置如何生效
1. `option/<name>.go` — 结构体定义
2. `protocol/<name>/` — 构造函数中解析配置
3. 运行时使用（连接建立、协议协商等）

### 模式 3：追踪协议注册链
1. `protocol/<name>/inbound.go` — `RegisterInbound(registry)` 定义
2. `include/registry.go` — `InboundRegistry()` 调用注册
3. `include/registry.go` — `Context()` 组装所有 Registry
4. `box.go` — `box.Context()` 将 Registry 注入 context
5. `box.go` — `box.New()` 中通过 Registry 创建实例

### 模式 4：分析接口满足性
```go
// 编译时接口断言模式（项目中广泛使用）
var _ adapter.TCPInjectableInbound = (*Inbound)(nil)
var _ adapter.UDPInjectableInbound = (*Inbound)(nil)
var _ adapter.Outbound = (*Outbound)(nil)
```

查找某个接口的所有实现者：
```bash
grep -rn "var _ adapter.TCPInjectableInbound" --include="*.go" protocol/
```

## 代码质量检查
```bash
golangci-lint run ./...
go vet ./...
go build -gcflags="-m" ./... 2>&1 | grep "escapes to heap"  # 逃逸分析
```

## sing-box 特有分析技巧

### 查找协议类型常量
```bash
grep -n "Type" constant/proxy.go
```

### 查找 Option 嵌入关系
```bash
# 查看哪些 Option 嵌入了 ListenOptions
grep -rn "ListenOptions$" option/ --include="*.go"
# 查看哪些 Option 嵌入了 DialerOptions
grep -rn "DialerOptions$" option/ --include="*.go"
# 查看哪些 Option 使用了 TLS 容器
grep -rn "TLSOptionsContainer$" option/ --include="*.go"
```
