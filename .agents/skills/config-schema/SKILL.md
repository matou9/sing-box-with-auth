---
name: config-schema
description: >
  sing-box 配置系统变更流程。当需要修改或添加 JSON 配置选项时使用。
  确保配置变更的完整性和向后兼容性。
---

# 配置变更流程

## 变更三步法

### 1. 修改 option/ 结构体
- 字段名 PascalCase，JSON tag snake_case
- 可选字段加 `omitempty`
- 新字段必须有合理的零值行为
- 示例：
  ```go
  type NewField struct {
      Enabled bool   `json:"enabled,omitempty"`
      Value   string `json:"value,omitempty"`
  }
  ```

### 2. 更新使用方代码
- protocol/ 或 route/ 中读取新配置
- 验证配置合法性
- 处理零值/默认值

### 3. 更新文档
- docs/configuration/ 下对应的 Markdown
- 包含字段说明、类型、默认值、示例

## 向后兼容规则
- 新增可选字段（omitempty）
- 为已有字段添加新的可选值
- 不得删除已有字段
- 不得改变字段类型
- 不得改变字段的 JSON 名称
- 改变默认行为需要明确记录在 changelog

## 常用嵌入类型

### Inbound 配置嵌入
```go
type XxxInboundOptions struct {
    ListenOptions                            // 基础监听配置（listen, listen_port, ...）
    InboundTLSOptionsContainer               // TLS 配置容器（可选）
    Transport *V2RayTransportOptions         // V2Ray Transport（可选）
    Multiplex *InboundMultiplexOptions       // 多路复用（可选）
    // 协议特有字段...
}
```

### Outbound 配置嵌入
```go
type XxxOutboundOptions struct {
    DialerOptions                            // 拨号配置（detour, bind_interface, ...）
    ServerOptions                            // 服务器地址（server, server_port）
    OutboundTLSOptionsContainer              // TLS 配置容器（可选）
    Transport *V2RayTransportOptions         // V2Ray Transport（可选）
    Network   NetworkList                    // 网络类型限制（可选）
    Multiplex *OutboundMultiplexOptions      // 多路复用（可选）
    // 协议特有字段...
}
```

## TLS 容器模式

### InboundTLSOptionsContainer
```go
// option/tls.go
type InboundTLSOptionsContainer struct {
    TLS *InboundTLSOptions `json:"tls,omitempty"`
}
```

### OutboundTLSOptionsContainer
```go
// option/tls.go
type OutboundTLSOptionsContainer struct {
    TLS *OutboundTLSOptions `json:"tls,omitempty"`
}
```

### InboundTLSOptions 关键字段
```go
type InboundTLSOptions struct {
    Enabled         bool                       `json:"enabled,omitempty"`
    ServerName      string                     `json:"server_name,omitempty"`
    CertificatePath string                     `json:"certificate_path,omitempty"`
    KeyPath         string                     `json:"key_path,omitempty"`
    ALPN            badoption.Listable[string] `json:"alpn,omitempty"`
    ACME            *InboundACMEOptions        `json:"acme,omitempty"`
    ECH             *InboundECHOptions         `json:"ech,omitempty"`
    Reality         *InboundRealityOptions     `json:"reality,omitempty"`
}
```

### OutboundTLSOptions 关键字段
```go
type OutboundTLSOptions struct {
    Enabled         bool                       `json:"enabled,omitempty"`
    DisableSNI      bool                       `json:"disable_sni,omitempty"`
    ServerName      string                     `json:"server_name,omitempty"`
    Insecure        bool                       `json:"insecure,omitempty"`
    CertificatePath string                     `json:"certificate_path,omitempty"`
    UTLS            *OutboundUTLSOptions       `json:"utls,omitempty"`
    Reality         *OutboundRealityOptions    `json:"reality,omitempty"`
}
```

## V2RayTransportOptions
```go
// option/v2ray_transport.go
type V2RayTransportOptions struct {
    Type string `json:"type"`  // "http", "ws", "quic", "grpc", "httpupgrade"
    // 内部根据 Type 选择对应的 Options struct
}
```

## NetworkList
```go
// option/types.go — 支持 string 或 []string 的 JSON 输入
type NetworkList string

func (v NetworkList) Build() []string  // 返回 ["tcp", "udp"]
```

## ServerOptions 使用
```go
// option/outbound.go
type ServerOptions struct {
    Server     string `json:"server"`
    ServerPort uint16 `json:"server_port"`
}

// 使用：
addr := options.ServerOptions.Build()      // 返回 M.Socksaddr
isDomain := options.ServerIsDomain()       // 判断是否为域名
```

## 路由规则配置
```go
// option/rule.go — 路由规则的配置结构
type Rule struct {
    Type           string      `json:"type,omitempty"`      // "", "default", "logical"
    DefaultOptions DefaultRule `json:"-"`
    LogicalOptions LogicalRule `json:"-"`
}

type DefaultRule struct {
    RawDefaultRule
    RuleAction
}

type RuleAction struct {
    Action       string             `json:"action,omitempty"`     // "route", "return", "reject"
    RouteOptions RouteActionOptions `json:"-"`
}

type RouteActionOptions struct {
    Outbound string `json:"outbound,omitempty"`
}
```
