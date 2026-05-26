package trafficquota

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
)

// Benchmarks for Unit 6 of the 2026-05-14 memory-leak plan: confirm that
// the lifecycle RWMutex introduced around RegisterConn / RemoveConfig
// does not regress the connection-registration hot path beyond the 5%
// p99 budget the plan calls out. Two structural concerns drive the
// shape of these benchmarks:
//
//  1. RegisterConn is read-side only — the lifecycle.RLock should let
//     many concurrent registrations proceed in parallel.
//     BenchmarkRegisterConn{,Parallel} measure that scaling.
//
//  2. RemoveConfig takes lifecycle.Lock briefly, which blocks ALL
//     RegisterConn calls (the lock is manager-wide, not per-user).
//     BenchmarkRegisterConnUnderRemoveConfigChurn deliberately injects
//     config churn on a separate user to expose any global stall in
//     other users' hot path. If the post-fix ns/op here is meaningfully
//     above the no-churn case, plan Risks → "改为 per-user/striped
//     lifecycle 锁" kicks in.

func newBenchManager(b *testing.B, users ...string) *QuotaManager {
	b.Helper()
	userConfigs := make([]option.TrafficQuotaUser, len(users))
	for i, name := range users {
		userConfigs[i] = option.TrafficQuotaUser{
			Name:    name,
			QuotaGB: quotaGB(1 << 40), // effectively unbounded; never trip during the bench
			Period:  "daily",
		}
	}
	manager, err := NewQuotaManager(option.TrafficQuotaServiceOptions{
		Users: userConfigs,
	})
	if err != nil {
		b.Fatalf("new manager: %v", err)
	}
	return manager
}

// BenchmarkRegisterConn measures the single-goroutine cost of one full
// RegisterConn → onClose cycle. This is the floor: any future change
// can move ns/op up here even without contention. Captures lifecycle
// RLock acquire/release + activeConns LoadOrStore + connList.add + the
// onClose closure's connList.remove.
func BenchmarkRegisterConn(b *testing.B) {
	manager := newBenchManager(b, "alice")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn := &stubQuotaTrackedConn{}
		_, onClose := manager.RegisterConn("alice", conn)
		onClose()
	}
}

// BenchmarkRegisterConnParallel exercises the lifecycle.RLock under
// goroutine parallelism. RWMutex read-sharing means many goroutines
// should make progress concurrently; if this benchmark scales poorly
// with -cpu=N>1 the lock has unexpected contention (e.g. a write-lock
// holder elsewhere, or sync.RWMutex starvation under the runtime).
func BenchmarkRegisterConnParallel(b *testing.B) {
	manager := newBenchManager(b, "alice")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn := &stubQuotaTrackedConn{}
			_, onClose := manager.RegisterConn("alice", conn)
			onClose()
		}
	})
}

// BenchmarkRegisterConnUnderRemoveConfigChurn measures user-A's
// RegisterConn throughput while a background goroutine churns
// ApplyConfig/RemoveConfig on user-B. The point is to expose any
// stall in user-A caused by user-B's lifecycle.Lock acquisition.
// Plan Risks calls out this exact concern; if numbers regress
// significantly vs BenchmarkRegisterConnParallel, the lifecycle lock
// needs to become per-user/striped.
func BenchmarkRegisterConnUnderRemoveConfigChurn(b *testing.B) {
	manager := newBenchManager(b, "alice", "bob")
	stop := make(chan struct{})
	var churnOps atomic.Int64
	go func() {
		bobConfig := option.TrafficQuotaUser{
			Name:    "bob",
			QuotaGB: quotaGB(1 << 40),
			Period:  "daily",
		}
		for {
			select {
			case <-stop:
				return
			default:
				_ = manager.ApplyConfig(bobConfig)
				_ = manager.RemoveConfig("bob")
				churnOps.Add(1)
			}
		}
	}()
	// Give the churn loop a few cycles to enter steady state before
	// we start the measured loop.
	for churnOps.Load() < 100 {
		time.Sleep(time.Microsecond)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn := &stubQuotaTrackedConn{}
			_, onClose := manager.RegisterConn("alice", conn)
			if onClose != nil {
				onClose()
			}
		}
	})
	b.StopTimer()
	close(stop)
	b.ReportMetric(float64(churnOps.Load()), "churn-ops")
}

// BenchmarkAddBytesParallel measures the AddBytes hot path under high
// concurrency. Unit 4 moved the periodKey-and-pendingDelta mutation
// into the periodAccess critical section; this benchmark confirms the
// per-user mutex serialization does not collapse parallel throughput
// for a single user.
func BenchmarkAddBytesParallel(b *testing.B) {
	manager := newBenchManager(b, "alice")
	// Seed the userState so the first AddBytes does not incur the
	// one-time setPeriodKeyIfEmpty cost.
	manager.AddBytes("alice", 1)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			manager.AddBytes("alice", 64)
		}
	})
}

// registerConnNoLifecycle mirrors QuotaManager.RegisterConn but skips
// the lifecycle.RLock/RUnlock pair. It exists purely so the Unit 6
// post-fix benchmarks have an apples-to-apples "pre-fix" peer to
// compare against — the 5% p99 regression gate the plan calls out is
// only meaningful against a baseline that holds every other Unit 1-5
// change constant. The function is test-only (lives in _test.go) and
// must NEVER be called from production code paths: it re-introduces
// the orphan-conn race Unit 6 closes.
func registerConnNoLifecycle(m *QuotaManager, user string, conn quotaTrackedConn) (func(int), func()) {
	if _, loaded := m.loadConfig(user); !loaded {
		return nil, nil
	}
	state := m.stateFor(user)
	state.setPeriodKeyIfEmpty(m.mustPeriodKey(user, m.now()))
	cl, _ := m.activeConns.LoadOrStore(user, &connList{})
	cl.add(conn)
	exceeded := state.exceeded.Load()
	if exceeded {
		conn.markQuotaExceeded()
	}
	return func(n int) {
			m.AddBytes(user, n)
		}, func() {
			cl.remove(conn)
		}
}

// BenchmarkRegisterConnNoLifecycle is the pre-Unit-6 baseline peer:
// identical setup and workload as BenchmarkRegisterConn, but the
// lifecycle lock is bypassed. The ns/op difference between this and
// BenchmarkRegisterConn quantifies the cost of Unit 6's fix. The plan
// budget is < 5% p99 regression.
func BenchmarkRegisterConnNoLifecycle(b *testing.B) {
	manager := newBenchManager(b, "alice")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn := &stubQuotaTrackedConn{}
		_, onClose := registerConnNoLifecycle(manager, "alice", conn)
		onClose()
	}
}

// BenchmarkRegisterConnParallelNoLifecycle is the parallel-mode peer
// for BenchmarkRegisterConnParallel. Without the lifecycle.RLock the
// concurrent path has one fewer atomic on the hot path; the delta
// here is the actual cost of the RWMutex's reader fast path.
func BenchmarkRegisterConnParallelNoLifecycle(b *testing.B) {
	manager := newBenchManager(b, "alice")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn := &stubQuotaTrackedConn{}
			_, onClose := registerConnNoLifecycle(manager, "alice", conn)
			onClose()
		}
	})
}
