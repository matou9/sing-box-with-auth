---
name: refactor
description: 代码重构流程。手动调用 /refactor <目标> 时使用。
invocation: user
---

# 重构流程

## 步骤

### 1. 分析（explore-singbox + impact-analysis）
- 理解当前代码结构
- 识别所有需要修改的文件
- 确定安全的修改顺序（按依赖拓扑排序）

### 2. 计划确认
输出重构计划供用户确认：
- 修改文件清单
- 每个文件的预期变更
- 潜在风险点
- 预估工作量

### 3. 执行（go-refactor agent / Agent Team）
- 文件数 <= 5：使用 go-refactor agent
- 文件数 > 5：启动 Agent Team 并行重构
- 每改一个文件：go build → go test（增量验证）

### 4. 最终验证
- 全量 go build（含所有构建标签）
- 全量 go test
- golangci-lint
- git diff 人工审查
