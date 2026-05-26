---
name: fix-bug
description: 完整的 bug 修复流程。手动调用 /fix-bug <描述> 时使用。
invocation: user
---

# Bug 修复流程

## 接收参数
用户提供 bug 描述或 Issue 编号

## 执行步骤

### Phase 1: 定位（使用 explore-singbox agent）
- 根据 bug 描述搜索相关代码
- 追踪可能的问题路径
- 输出：根因分析 + 相关文件列表

### Phase 2: 影响评估（使用 impact-analysis skill）
- 确认修复方案
- 分析修改影响范围
- 输出：影响报告 + 修改计划

### Phase 3: 实施修复（使用对应领域 agent）
- 根据模块选择 protocol-engineer / network-engineer / route-engineer
- 执行最小化修改
- 输出：代码变更

### Phase 4: 验证（使用 test-engineer agent）
- 编写回归测试
- 运行相关测试套件
- 验证修复有效且无副作用
- 输出：测试报告

### Phase 5: 审查（使用 security-reviewer agent，如涉及安全）
- 审查修改的安全性
- 输出：审查意见
