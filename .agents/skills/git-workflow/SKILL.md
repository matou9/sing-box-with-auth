---
name: git-workflow
description: Git 分支管理和提交规范。提交代码、创建分支、管理 PR 时使用。
---

# Git 工作流

- 分支命名：feature/<name>, fix/<issue-id>, refactor/<name>
- Commit 格式：<type>(<scope>): <description>
  - type: feat, fix, refactor, docs, test, chore
  - scope: protocol, route, dns, transport, option, common
- 每个 commit 应能独立编译通过
- 功能分支从 dev-next 创建
