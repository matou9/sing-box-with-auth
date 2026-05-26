---
name: evolve
description: >
  技能进化技能。分析 evolution-log 和 skill-accuracy-tracker，
  找出低准确率 Skill，用真实代码模式重写其内容。
invocation: user
---

# 技能进化 /evolve

## 触发条件
当 evolution-log.md 积累了 3-5 条以上记录后，手动调用 `/evolve` 来更新 Skill。

## 执行步骤

### Step 1: 读取进化数据
1. 读取 `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\evolution-log.md`
2. 读取 `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\skill-accuracy-tracker.md`
3. 识别评分低于 B 的 Skill

### Step 2: 分析 Skill 差距
对每个低评分 Skill：
1. 读取当前 Skill 文件：`E:\test\sing-box\.Codex\skills\<name>\SKILL.md`
2. 读取 evolution-log 中关于该 Skill 的所有记录
3. 汇总所有已知问题：
   - 错误的类型名/函数名
   - 遗漏的步骤
   - 过时的模式

### Step 3: 验证真实模式
对每个需要修正的模式：
1. 使用 Grep 在代码库中验证正确的类型名/函数签名
2. 读取 2-3 个参考实现确认模式一致性
3. 对照记忆文件 `patterns-*.md` 确认

### Step 4: 重写 Skill
用真实模式重写 Skill 文件，确保：
1. 所有类型名与代码库一致
2. 所有函数签名与代码库一致
3. 代码模板可直接使用（复制后编译通过）
4. 包含清晰的步骤和检查清单

### Step 5: 验证重写结果
对重写后的 Skill：
1. 提取所有引用的类型名，用 Grep 验证每个都存在
2. 提取所有函数签名，用 Grep 验证匹配
3. 如有模板代码，检查 import 路径是否正确

### Step 6: 更新追踪文件
1. 更新 `skill-accuracy-tracker.md` — 提升评分、更新版本号
2. 更新 `evolution-metrics.md` — 增加进化次数
3. 在 `evolution-log.md` 中记录本次进化操作

## 输出格式

```
## 进化报告

### 分析结果
| Skill | 旧评分 | 问题数 | 优先级 |
|-------|--------|--------|--------|
| ... | D | 5 | 高 |

### 执行的更新
1. **skill-name** (v1 → v2)
   - 修正: [具体描述]
   - 验证: [grep 结果]

### 更新后评分
| Skill | 新评分 | 验证状态 |
|-------|--------|---------|
| ... | A | ✅ 通过 |

### 已更新的文件
- [ ] .Codex/skills/<name>/SKILL.md
- [ ] skill-accuracy-tracker.md
- [ ] evolution-metrics.md
- [ ] evolution-log.md
```

## 注意事项
- 不要凭记忆修改 Skill，必须从代码库中验证
- 每次只更新 1-3 个 Skill，确保质量
- 保持 Skill 的简洁性，避免信息过载
