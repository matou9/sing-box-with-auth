---
name: verify
description: >
  修改后自动验证技能。执行编译检查、运行测试、分析结果，
  失败时自动提取错误模式写入记忆。手动调用 /verify 时使用。
invocation: user
---

# 修改后验证 /verify

## 触发条件
- 手动调用 `/verify` 或 `/verify <测试范围>`
- 在完成代码修改后使用，验证修改的正确性

## 上下文准备

1. Read `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\session-state.md` — 获取计划信息
2. Read `C:\Users\38529\.Codex\projects\E--test-sing-box\memory\task-state.md` — 获取任务进度
3. 更新 `session-state.md` phase 为 `verifying`

## 执行步骤

### Step 1: 检测修改范围
```bash
# 获取当前修改的文件列表
git diff --name-only
git diff --cached --name-only
```

根据修改的文件自动判断验证策略：

| 修改目录 | 验证策略 |
|----------|----------|
| `protocol/<name>/` | 编译 + 该协议测试 |
| `option/` | 编译 + 相关协议测试 |
| `route/` 或 `dns/` | 编译 + 全量测试 |
| `common/` | 编译 + 全量测试 + 竞态检测 |
| `adapter/` | 编译 + 全量测试 |
| `include/` | 编译 + 相关功能测试 |
| `test/` | 仅运行测试 |
| `docs/` | 仅文档格式检查 |

### Step 2: 编译检查（必选）
```bash
go build -tags "with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_tailscale,with_ccm,badlinkname,tfogo_checklinkname0" ./...
```

**如果编译失败**：
1. 解析错误信息，提取：
   - 错误类型（类型不匹配、未定义、导入错误等）
   - 错误文件和行号
   - 错误的代码片段
2. 尝试自动修复常见编译错误：
   - 缺少 import → 添加 import
   - 类型不匹配 → 检查正确的类型（参考 MEMORY.md）
   - 未定义的符号 → 用 grep 查找正确的位置
3. 修复后重新编译

### Step 3: 运行测试（按修改范围）

**单协议测试**:
```bash
cd test && go test -v -tags "with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api" -run "Test<Name>" ./...
```

**全量测试**:
```bash
cd test && go test -v -tags "with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api" ./...
```

**竞态检测（common/ 修改时）**:
```bash
cd test && go test -race -tags "with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api" ./...
```

### Step 4: 结果分析

**编译成功 + 测试通过**:
```markdown
## ✅ 验证通过

- 编译: 通过
- 测试: X/X 通过
- 耗时: Xs

建议运行 `/reflect` 记录此次成功经验。
```

**编译成功 + 测试失败**:
1. 解析失败的测试用例
2. 提取失败原因（超时、断言失败、连接错误等）
3. 判断是否与本次修改相关
4. 输出诊断报告：

```markdown
## ❌ 测试失败

### 失败的测试
| 测试名 | 失败原因 | 与本次修改相关 |
|--------|----------|---------------|
| TestXxx | 超时 | 是/否 |

### 错误详情
[错误日志摘要]

### 建议修复方向
1. [具体建议]
```

**编译失败**:
```markdown
## ❌ 编译失败

### 错误列表
| 文件:行 | 错误类型 | 错误信息 |
|---------|---------|---------|
| xxx.go:42 | 类型错误 | ... |

### 自动修复尝试
- [尝试了什么]
- [结果如何]

### 手动修复建议
1. [具体建议]
```

### Step 5: 失败模式记录
**仅在失败时执行**。将失败模式写入记忆：

1. 更新 `evolution-log.md`：
```markdown
### YYYY-MM-DD | /verify 失败记录
- **结果**: 编译失败/测试失败
- **错误模式**: [错误的分类描述]
- **根本原因**: [分析得出的原因]
- **修复方法**: [如何修复的]
```

2. 如果错误与 Skill 模板有关，更新 `skill-accuracy-tracker.md` 降低评分

3. 如果发现新的代码模式，更新对应的 `patterns-*.md`

### Step 6: 写入验证结果到 session-state.md

更新 `session-state.md` 的验证结果部分：
```markdown
## 验证结果（由 /verify 写入）
- 时间: YYYY-MM-DD HH:MM
- 编译: 通过/失败
- 测试: X/Y 通过
- 失败模式: [描述或无]
- 建议: /reflect
```

同时更新 `task-state.md` 标记验证步骤完成。

### Step 7: 输出验证报告

```markdown
## 验证报告

### 环境
- 修改范围: [文件列表]
- 验证策略: [编译/单测/全量/竞态]

### 结果
- 编译: ✅/❌
- 测试: ✅ X/X 通过 / ❌ X 失败
- 竞态检测: ✅/❌/未执行

### 失败模式（如有）
- [模式描述]
- [已写入记忆: 是/否]

### 后续建议
- [ ] 运行 `/reflect` 反思
- [ ] 修复失败项后重新 `/verify`
```

## 与其他技能的衔接

```
代码修改 → /verify → 通过? → /reflect（记录成功）
                    → 失败? → 修复 → /verify（重试）→ /reflect（记录学习）
```

## 快速验证模式

如果只想快速检查编译：
```
/verify build
```

如果只想运行特定测试：
```
/verify test TestShadowsocks
```
