---
name: review
description: 代码审查。手动调用 /review [文件/分支] 时使用。
invocation: user
---

# 代码审查流程

## 审查范围
- 指定文件：审查特定文件的最新改动
- 指定分支：审查分支与 dev-next 的差异
- 未指定：审查当前未提交的改动

## 获取改动
```bash
# 未提交的改动
git diff
git diff --cached

# 分支差异
git diff dev-next...<branch>

# 特定文件历史
git log -5 --patch <file>
```

## 审查维度
使用 security-reviewer agent 进行安全审查，同时检查：
1. **正确性**：逻辑是否正确，边界条件是否处理
2. **安全性**：是否有安全漏洞
3. **性能**：是否有性能退化
4. **兼容性**：是否破坏向后兼容
5. **代码质量**：是否符合项目规范
6. **测试**：是否有充分的测试覆盖
