---
name: cross-platform
description: 跨平台代码开发指南。处理平台差异代码时使用。
---

# 跨平台开发指南

- 文件后缀约定：_linux.go, _darwin.go, _windows.go, _android.go
- 构建标签：//go:build linux && !android
- 平台特定 API 封装在 common/ 或 protocol/ 对应平台文件中
- TUN 实现差异最大：Linux(tun_linux.go) vs macOS vs Windows
- 网络接口操作：iproute2(Linux), ifconfig(macOS), netsh(Windows)
- 测试时注意标注平台约束
