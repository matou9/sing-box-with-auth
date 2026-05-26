---
name: plan-task
description: >
  需求分析与实施计划生成。接收需求描述，自动探索相关代码，
  生成分步实施计划并评估影响范围。手动调用 /plan-task <需求> 时使用。
invocation: user
---

# 需求分析与计划生成 /plan-task

## 触发条件
手动调用 `/plan-task <需求描述>` 开始一个完整的开发任务。

## 上下文准备（必须先执行！）

1. Read `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\session-state.md` — 检查是否有活跃任务
2. Read `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\task-state.md` — 检查是否有未完成任务
3. 如果有未完成任务，提示用户：是继续还是开始新任务
4. 根据任务类型 Read 对应的 patterns 文件（见「常用探索模板」）

## 执行步骤

### Step 0: 初始化状态
更新 `session-state.md`：
```markdown
## 当前状态
phase: planning
```

### Step 1: 需求解析
从用户输入中提取：
1. **任务类型**: 新功能 / Bug 修复 / 重构 / 配置变更 / 文档更新
2. **涉及的模块**: protocol / route / dns / option / common / include / test
3. **核心目标**: 一句话描述期望结果
4. **约束条件**: 向后兼容性、构建标签、平台限制

### Step 2: 代码探索
根据任务类型自动探索相关代码：

**新协议/功能**:
```bash
# 找类似的实现作为参考
grep -rn "Register\[" include/registry.go
# 找相关的 Option 结构
grep -rn "type.*Options struct" option/
# 找相关的接口实现
grep -rn "var _ adapter\." protocol/ --include="*.go"
```

**Bug 修复**:
```bash
# 定位错误来源
grep -rn "<error_keyword>" --include="*.go" .
# 追踪调用链
grep -rn "<function_name>" --include="*.go" .
```

**配置变更**:
```bash
# 找当前配置结构
grep -rn "type.*Options struct" option/<module>.go
# 找使用方
grep -rn "<OptionType>" --include="*.go" .
```

### Step 3: 影响分析
1. 列出所有需要修改的文件
2. 评估每个文件的修改范围（新增/修改/删除多少行）
3. 识别潜在的连带影响：
   - 修改 `adapter/` → 可能影响所有协议实现
   - 修改 `option/` → 可能影响配置解析和文档
   - 修改 `common/` → 可能影响全局
   - 修改 `route/` → 可能影响路由和 DNS

### Step 4: 生成实施计划
输出结构化的实施计划：

```markdown
## 实施计划

### 任务概述
- **类型**: [新功能/Bug修复/重构/配置变更]
- **目标**: [一句话描述]
- **参考实现**: [相似的现有代码路径]

### 修改清单（按执行顺序）

#### 1. [文件路径]
- 修改内容：[描述]
- 原因：[为什么需要修改]
- 参考：[相关代码片段或文件]

#### 2. [文件路径]
...

### 依赖关系
- Step N 依赖 Step M（原因：...）

### 验证步骤
1. `go build -tags "..." ./...` — 编译检查
2. `cd test && go test -v -tags "..." -run TestXxx ./...` — 相关测试
3. [其他验证步骤]

### 风险评估
- **向后兼容性**: [安全/需要注意/有破坏性]
- **影响范围**: [局部/中等/广泛]
- **测试覆盖**: [已有测试/需要新增测试/无法自动测试]
```

### Step 5: 持久化计划到状态文件
将计划写入两个文件：

**session-state.md** — 写入计划摘要供后续 Skill 使用：
```markdown
## 计划（由 /plan-task 写入）
- 任务: [描述]
- 类型: [类型]
- 步骤数: N
- 涉及文件: [列表]
- 需要的 patterns: [列表]
```

**task-state.md** — 写入完整步骤供 compact 后恢复：
```markdown
## 当前任务
status: in_progress
task: [描述]
type: [类型]
started: [时间]

## 步骤进度
- [ ] Step 1: ...
- [ ] Step 2: ...
...

## 上下文恢复提示
需要 Read 的 patterns 文件: [列表]
参考实现: [路径]
```

### Step 6: 创建任务追踪
使用 TaskCreate 为每个修改步骤创建任务，设置依赖关系：
```
TaskCreate: Step 1 — 修改 xxx
TaskCreate: Step 2 — 修改 yyy（blockedBy: Step 1）
...
TaskCreate: Step N+1 — 运行 /verify 验证
TaskCreate: Step N+2 — 运行 /reflect 反思
```

### Step 7: 等待确认
将计划呈现给用户，等待确认后：
- 更新 `session-state.md` phase 为 `executing`
- 开始执行第一步

## 与其他技能的衔接

```
/plan-task → 生成计划 → 用户确认 → 逐步执行 → /verify → /reflect
```

- 执行阶段自动加载相关 Skill（protocol-dev, config-schema 等）
- 完成后自动建议运行 `/verify` 验证
- 验证通过后自动建议运行 `/reflect` 反思

## 常用探索模板

### 协议开发任务
```
探索: constant/proxy.go, option/, protocol/<参考协议>/, include/registry.go, test/
参考 Skill: protocol-dev, add-protocol
```

### 路由规则任务
```
探索: option/rule.go, route/rule/, adapter/router.go
参考 Skill: config-schema
```

### DNS 相关任务
```
探索: option/dns.go, dns/, adapter/dns.go
参考 Skill: config-schema
```

### 性能优化任务
```
探索: common/, protocol/<目标>/, route/
参考 Skill: perf-optimize
```
