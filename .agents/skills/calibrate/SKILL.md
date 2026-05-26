---
name: calibrate
description: >
  Agent 校准技能。分析 Agent 委派历史和 Skill 使用模式，
  调优 Agent 描述以提高自动选择精度。
invocation: user
---

# Agent 校准 /calibrate

## 触发条件
当 evolution-log.md 积累了 10+ 条记录后，手动调用 `/calibrate` 来调优 Agent。

## 执行步骤

### Step 1: 读取使用数据
1. 读取 `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\evolution-log.md`
2. 读取 `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\evolution-metrics.md`
3. 读取 `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\skill-accuracy-tracker.md`

### Step 2: 分析 Agent 配置
读取所有 Agent 定义文件：
```
E:\test\sing-box\.Codex\agents\*.md
```

对每个 Agent 分析：
1. **触发精度**: Agent 是否在正确的场景下被选中？
2. **Skill 映射**: Agent 引用的 Skill 是否与实际使用匹配？
3. **描述清晰度**: Agent 描述是否足够区分不同 Agent 的职责？

### Step 3: 识别改进点
常见问题：
- Agent 描述过于笼统，导致错误选择
- Agent 之间职责重叠
- Agent 缺少关键的自动触发条件
- Agent 引用了过时或错误的 Skill

### Step 4: 调优 Agent 描述
对需要改进的 Agent：
1. 精化触发条件描述
2. 明确排除条件
3. 更新引用的 Skill 列表
4. 添加使用场景示例

### Step 5: 验证改进
1. 模拟几个典型任务场景
2. 检查改进后的 Agent 描述是否会匹配正确的场景
3. 确认没有引入新的歧义

### Step 6: 更新追踪
1. 在 `evolution-log.md` 中记录校准操作
2. 更新 `evolution-metrics.md` 中的校准次数

## 输出格式

```
## 校准报告

### Agent 使用分析
| Agent | 调用次数 | 正确选择率 | 问题 |
|-------|---------|-----------|------|
| ... | N | 80% | 描述 |

### 执行的调优
1. **agent-name**
   - 问题: [描述]
   - 改进: [具体修改]

### 建议
1. [后续优化建议]

### 已更新的文件
- [ ] .Codex/agents/<name>.md
- [ ] evolution-log.md
- [ ] evolution-metrics.md
```

## 注意事项
- 校准基于实际使用数据，不要凭想象调整
- 保持 Agent 描述简洁，避免过度描述
- 确保 Agent 之间职责边界清晰
- 每次校准后观察 2-3 个任务验证效果
