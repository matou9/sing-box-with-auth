---
name: explore
description: 快速探索 sing-box 代码库。手动调用 /explore 时使用。
invocation: user
---

使用 explore-singbox agent 来探索用户描述的代码区域。

步骤：
1. 解析用户要探索的目标（模块、函数、特性）
2. 委派给 explore-singbox agent 执行深度搜索
3. 汇总结果，以结构化格式呈现：
   - 相关文件列表（路径 + 简述）
   - 核心代码片段
   - 模块关系图
   - 建议的进一步探索方向
