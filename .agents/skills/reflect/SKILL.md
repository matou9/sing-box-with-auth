---
name: reflect
description: >
  任务后反思技能。完成开发任务后使用，记录学到的模式、失败原因，
  更新 evolution-log 和 skill-accuracy-tracker。支持自动和手动触发。
invocation: user
---

# 任务后反思 /reflect

## 触发条件
- 手动调用 `/reflect` — 完成任务后主动反思
- 由 `/verify` 建议触发 — 验证通过或失败后
- 由 Hooks 自动触发 — Stop 事件时作为建议

## 上下文准备

1. Read `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\session-state.md` — 获取计划和验证结果
2. Read `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\task-state.md` — 获取任务进度
3. 更新 `session-state.md` phase 为 `reflecting`

## 执行步骤

### Step 1: 收集任务信息
1. 从 `session-state.md` 读取计划和验证结果（如有，无需重复分析）
2. 运行 `git diff HEAD~1` 或 `git diff` 查看最近的代码变更
3. 回顾当前会话中执行的操作和遇到的问题
4. 识别使用了哪些 Skill，哪些模式是正确的，哪些需要修正

### Step 2: 分析模式差异
对于每个使用的 Skill：
1. 对比 Skill 提供的模板与实际写出的代码
2. 标记差异点：
   - **类型错误**: 使用了不存在的类型/接口
   - **签名错误**: 函数签名与实际不符
   - **模式错误**: 推荐的模式与项目约定不符
   - **遗漏**: Skill 缺少关键步骤
   - **过时**: Skill 内容与当前代码版本不符

### Step 3: 分析测试/编译结果（如有）
如果本次任务包含了 `/verify` 或手动测试：

**编译错误分析**:
- 提取错误模式分类：
  - `undefined: xxx` → 类型/函数名错误
  - `cannot use xxx as yyy` → 接口不匹配
  - `imported and not used` → 多余的 import
  - `missing method xxx` → 接口实现不完整
- 判断错误是否源于 Skill 模板提供的错误信息

**测试失败分析**:
- 提取失败模式分类：
  - `timeout` → 连接/启动超时
  - `connection refused` → 端口冲突或服务未启动
  - `assertion failed` → 逻辑错误
  - `panic` → 空指针/越界
  - `race detected` → 竞态条件
- 判断是否为已知问题或新问题
- 检查端口常量是否与其他测试文件冲突

**失败模式记忆**:
如果发现可复现的失败模式，写入 `patterns-testing.md` 的「常见测试陷阱」部分：
```markdown
### 常见测试陷阱
- 端口 XXXXX 已被 test/yyy_test.go 使用，不要重复
- TLS 测试需要先生成证书，使用 `createSelfSignedCertificate()`
- 包含 DNS 的测试需要 with_dhcp 标签
```

### Step 4: 更新进化日志
向 `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\evolution-log.md` 追加条目：

```markdown
### YYYY-MM-DD | 任务描述
- **结果**: 成功/失败/部分成功
- **使用的 Skill**: skill-name-1, skill-name-2
- **编译结果**: 通过/失败（错误类型）
- **测试结果**: 通过/失败（失败数/总数）
- **学到的模式**: 具体描述
- **Skill 差距**: 具体描述差异
- **修正操作**: 描述需要的修正
- **失败模式**（如有）: 错误分类 + 根因分析
```

### Step 5: 更新准确率追踪
更新 `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\skill-accuracy-tracker.md` 中相关 Skill 的评分：

| 情况 | 评分调整 |
|------|----------|
| Skill 模板直接可用，编译+测试通过 | 维持 A |
| Skill 模板需小修改（<3处） | B |
| Skill 模板有显著错误（3-5处） | C |
| Skill 模板严重不匹配 | D |
| Skill 模板导致编译失败 | F |

如果某个 Skill 连续 3 次评分低于 B，在建议中标记「需要 /evolve」。

### Step 6: 更新记忆文件（如发现新模式）
如果在任务中发现了记忆文件中未记录的新模式：
1. 确认该模式在代码中确实存在（grep 验证）
2. 更新对应的 `patterns-*.md` 文件
3. 如果是关键规则，同步更新 `MEMORY.md`（保持 <200 行）

### Step 7: 更新指标
更新 `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\evolution-metrics.md` 中的统计数据：
- 总任务数 +1
- 成功率重新计算
- Skill 使用次数更新
- 记忆文件更新次数（如有）

## 输出格式

```
## 反思报告

### 任务概述
[简述完成的任务]

### 验证结果
- 编译: ✅/❌ [详情]
- 测试: ✅/❌ [X/Y 通过]

### 使用的 Skill 及评估
| Skill | 评分 | 变化 | 问题 |
|-------|------|------|------|
| ... | A→A | → | 无 |
| ... | A→C | ↓ | 描述 |

### 失败模式分析（如有）
| 错误类型 | 出现次数 | 根因 | 已记录到记忆 |
|----------|---------|------|-------------|
| ... | N | 描述 | 是/否 |

### 新发现的模式
1. [描述]

### 建议的后续操作
- [ ] /evolve [skill-name]（如果评分低于 B）
- [ ] 更新 MEMORY.md（如果发现关键规则）
- [ ] /calibrate（如果积累了 10+ 条记录）

### 已更新的文件
- [x] evolution-log.md
- [x] skill-accuracy-tracker.md
- [x] evolution-metrics.md
- [ ] patterns-*.md（如有新模式）
- [ ] MEMORY.md（如有关键规则）
```

### Step 8: 清理会话状态
反思完成后：

1. 更新 `session-state.md`：
```markdown
## 当前状态
phase: idle

## 反思结论（由 /reflect 写入）
- 时间: YYYY-MM-DD
- 任务: [描述]
- 结果: 成功/失败
- Skill 评分变化: [列表]
- 建议后续: /evolve [skill] 或无
```

2. 更新 `task-state.md`：
   - 如果任务全部完成：将 status 设为 `completed`
   - 如果任务未完成：保持 `in_progress`，记录当前进度

3. 清空 `session-state.md` 的计划和验证结果部分（保留反思结论供下次参考）

## 与闭环的衔接

```
/plan-task → 执行修改 → /verify → /reflect → evolution-log
     ↑          ↑           ↑         ↑
     │    task-state.md  session-state.md
     │    (进度持久化)    (状态传递)
     │                                ↓
     │                积累 3+ 条同一 Skill 低分
     │                                ↓
     └───── 下次任务 ←──── /evolve ◄──┘
```
