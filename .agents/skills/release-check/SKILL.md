---
name: release-check
description: 版本发布前的检查清单。准备发布新版本时使用。
---

# 发布检查清单

- [ ] 所有测试通过（含构建标签变体）
- [ ] golangci-lint 无新告警
- [ ] go mod tidy 干净
- [ ] 文档已更新
- [ ] CHANGELOG 已更新
- [ ] 向后兼容性已验证
- [ ] 多平台构建成功（Makefile targets）
