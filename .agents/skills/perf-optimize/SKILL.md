---
name: perf-optimize
description: Go 性能优化技能。优化代码性能、减少内存分配、分析瓶颈时使用。
---

# 性能优化指南

- pprof 性能分析：cpu / mem / goroutine / block
- 基准测试：go test -bench -benchmem
- 逃逸分析：go build -gcflags="-m"
- sync.Pool 复用 buffer
- 减少不必要的 interface{} 装箱
- splice / zero-copy 技术（Linux）
