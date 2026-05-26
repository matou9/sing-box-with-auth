---
name: debug-network
description: 网络代理调试技能。排查连接问题、DNS 泄漏、路由异常时使用。
---

# 网络调试指南

## sing-box 内置调试工具

### 配置验证
```bash
# 验证配置文件语法和结构
sing-box check -c config.json
# 或
sing-box check -D /etc/sing-box
```

### 日志级别调整
```json
{
    "log": {
        "level": "trace",
        "timestamp": true
    }
}
```
级别从低到高：`trace` → `debug` → `info` → `warn` → `error` → `fatal` → `panic`

**调试时使用 `trace` 级别** 可以看到完整的连接建立、规则匹配、DNS 解析过程。

### Clash API Dashboard
```json
{
    "experimental": {
        "clash_api": {
            "external_controller": "127.0.0.1:9090",
            "external_ui": "ui",
            "secret": ""
        }
    }
}
```
然后访问 `http://127.0.0.1:9090/ui` 查看实时连接、流量统计。

API 端点：
- `GET /connections` — 查看活跃连接
- `GET /proxies` — 查看代理节点状态
- `GET /rules` — 查看路由规则
- `GET /logs` — 实时日志流

## 常见问题排查

### 连接超时
1. 检查日志中的 `dial` 错误
2. 确认服务器地址和端口正确
3. 检查 TLS 配置（server_name 是否匹配）
4. 检查出站 dialer 配置（bind_interface, routing_mark）

### TLS 握手失败
1. 检查证书路径是否正确
2. 确认 server_name 与证书匹配
3. 检查 ALPN 协商
4. 尝试 `insecure: true` 排除证书问题（仅调试用）

### DNS 泄漏
1. 检查 DNS 路由规则是否正确
2. 确认 `domain_resolver` 配置
3. 使用 `trace` 日志查看 DNS 查询路径
4. 检查是否有规则绕过了 DNS 代理

### 路由异常
1. 使用 `trace` 日志查看规则匹配过程
2. 检查规则优先级（按顺序匹配，首条匹配生效）
3. 确认 `final` 出站配置
4. 检查 inbound tag 是否正确

### 认证失败
1. 检查用户名/密码/UUID 是否一致
2. 确认加密方法匹配
3. 检查时间同步（VMess 需要）

## 抓包分析

### tcpdump
```bash
# 捕获特定端口的流量
tcpdump -i any port 1080 -w capture.pcap
# 捕获特定接口
tcpdump -i eth0 host 1.2.3.4 -w capture.pcap
```

### Wireshark 过滤器
```
# SOCKS5 流量
tcp.port == 1080
# TLS 握手
tls.handshake.type == 1
# 特定 SNI
tls.handshake.extensions_server_name == "example.com"
```

## Linux 路由调试
```bash
# 查看路由表
ip rule show
ip route show table all
# 查看 TUN 接口
ip addr show tun0
# 查看 iptables/nftables 规则
iptables -t mangle -L -v -n
nft list ruleset
```

## 日志关键模式

| 日志内容 | 含义 |
|---------|------|
| `inbound connection to X.X.X.X:443` | 新连接进入 |
| `[rule-N] match` | 路由规则匹配 |
| `open connection to X.X.X.X using outbound/xxx` | 出站连接建立 |
| `connection closed` | 正常关闭 |
| `handshake failure` | 协议握手失败 |
| `DNS query` | DNS 查询 |
