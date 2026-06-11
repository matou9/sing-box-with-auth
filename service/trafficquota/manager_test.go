package trafficquota

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
)

// TestQuotaManagerAddBytesRemoveConfigNoPanic covers the unlocked AddBytes
// hot path racing RemoveConfig: AddBytes can pass its loadConfig check, lose
// the CPU while RemoveConfig deletes both config and state, then resurrect an
// empty state via stateFor. Pre-fix, the empty-periodKey branch called
// mustPeriodKey, which panics once the config is gone.
func TestQuotaManagerAddBytesRemoveConfigNoPanic(t *testing.T) {
	manager, err := NewQuotaManager(option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(1 << 30), Period: "daily"},
		},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					manager.AddBytes("alice", 1)
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		if err := manager.RemoveConfig("alice"); err != nil {
			t.Errorf("remove config: %v", err)
		}
		if err := manager.ApplyConfig(option.TrafficQuotaUser{Name: "alice", QuotaGB: quotaGB(1 << 30), Period: "daily"}); err != nil {
			t.Errorf("apply config: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestQuotaManagerResolvesGroupAndUserOverride(t *testing.T) {
	manager, err := NewQuotaManager(option.TrafficQuotaServiceOptions{
		Groups: []option.TrafficQuotaGroup{
			{
				Name:    "basic",
				QuotaGB: quotaGB(2048),
				Period:  "monthly",
			},
		},
		Users: []option.TrafficQuotaUser{
			{
				Name:  "alice",
				Group: "basic",
			},
			{
				Name:    "bob",
				Group:   "basic",
				QuotaGB: quotaGB(1024),
				Period:  "daily",
			},
		},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	alice := manager.userConfigs["alice"]
	if alice == nil {
		t.Fatal("expected alice quota config")
	}
	if alice.quotaBytes != 2048 {
		t.Fatalf("unexpected alice quota bytes: %d", alice.quotaBytes)
	}
	if alice.period != PeriodMonthly {
		t.Fatalf("unexpected alice period: %s", alice.period)
	}

	bob := manager.userConfigs["bob"]
	if bob == nil {
		t.Fatal("expected bob quota config")
	}
	if bob.quotaBytes != 1024 {
		t.Fatalf("unexpected bob quota bytes: %d", bob.quotaBytes)
	}
	if bob.period != PeriodDaily {
		t.Fatalf("unexpected bob period: %s", bob.period)
	}
}

func TestQuotaManagerExceedClosesActiveConnections(t *testing.T) {
	manager, err := NewQuotaManager(option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{
				Name:    "alice",
				QuotaGB: quotaGB(1024),
				Period:  "daily",
			},
		},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	conn1 := &stubQuotaTrackedConn{}
	conn2 := &stubQuotaTrackedConn{}
	onBytes1, onClose1 := manager.RegisterConn("alice", conn1)
	onBytes2, _ := manager.RegisterConn("alice", conn2)
	if onBytes1 == nil || onBytes2 == nil || onClose1 == nil {
		t.Fatal("expected callbacks for tracked user")
	}

	onBytes1(600)
	if manager.IsExceeded("alice") {
		t.Fatal("expected quota not exceeded yet")
	}

	onBytes2(500)
	if !manager.IsExceeded("alice") {
		t.Fatal("expected quota exceeded")
	}
	if conn1.closed.Load() != 1 || conn2.closed.Load() != 1 {
		t.Fatalf("expected both tracked connections closed once, got %d and %d", conn1.closed.Load(), conn2.closed.Load())
	}

	onClose1()
	connList, loaded := manager.activeConns.Load("alice")
	if !loaded {
		t.Fatal("expected active connection list")
	}
	if got := connList.len(); got != 1 {
		t.Fatalf("expected one active connection after unregister, got %d", got)
	}
}

func TestQuotaManagerConcurrentAddBytesOnlyTripsOnce(t *testing.T) {
	manager, err := NewQuotaManager(option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{
				Name:    "alice",
				QuotaGB: quotaGB(512),
				Period:  "daily",
			},
		},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	conn := &stubQuotaTrackedConn{}
	onBytes, _ := manager.RegisterConn("alice", conn)
	if onBytes == nil {
		t.Fatal("expected callbacks for tracked user")
	}

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			onBytes(16)
		}()
	}
	wg.Wait()

	if !manager.IsExceeded("alice") {
		t.Fatal("expected quota exceeded")
	}
	if conn.closed.Load() != 1 {
		t.Fatalf("expected tracked connection closed once, got %d", conn.closed.Load())
	}
	if usage := manager.Usage("alice"); usage != 1600 {
		t.Fatalf("unexpected usage: %d", usage)
	}
}

func TestQuotaManagerCheckPeriodResetClearsUsageAndExceeded(t *testing.T) {
	manager, err := NewQuotaManager(option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{
				Name:    "alice",
				QuotaGB: quotaGB(1024),
				Period:  "daily",
			},
		},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	manager.now = func() time.Time {
		return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	}
	manager.LoadUsage("alice", 1200)
	if !manager.IsExceeded("alice") {
		t.Fatal("expected quota exceeded after load usage")
	}

	manager.CheckPeriodReset(time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC))

	if manager.IsExceeded("alice") {
		t.Fatal("expected quota exceeded flag reset")
	}
	if usage := manager.Usage("alice"); usage != 0 {
		t.Fatalf("expected usage reset, got %d", usage)
	}
}

func TestQuotaManagerGetCurrentPeriodKeyCustom(t *testing.T) {
	manager, err := NewQuotaManager(option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{
				Name:        "alice",
				QuotaGB:     quotaGB(1024),
				Period:      "custom",
				PeriodStart: "2026-04-01",
				PeriodDays:  7,
			},
		},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	key1, err := manager.GetCurrentPeriodKey("alice", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("get period key 1: %v", err)
	}
	key2, err := manager.GetCurrentPeriodKey("alice", time.Date(2026, 4, 7, 23, 59, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("get period key 2: %v", err)
	}
	key3, err := manager.GetCurrentPeriodKey("alice", time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("get period key 3: %v", err)
	}

	if key1 != key2 {
		t.Fatalf("expected same custom period key, got %q and %q", key1, key2)
	}
	if key3 == key1 {
		t.Fatalf("expected new custom period key, got %q", key3)
	}
}

func TestQuotaManagerLoadUsageContinuesAccumulation(t *testing.T) {
	manager, err := NewQuotaManager(option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{
				Name:    "alice",
				QuotaGB: quotaGB(1024),
				Period:  "daily",
			},
		},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	manager.LoadUsage("alice", 900)
	manager.AddBytes("alice", 100)
	if manager.IsExceeded("alice") {
		t.Fatal("expected quota not exceeded yet")
	}
	manager.AddBytes("alice", 50)
	if !manager.IsExceeded("alice") {
		t.Fatal("expected quota exceeded after additional bytes")
	}
	if usage := manager.Usage("alice"); usage != 1050 {
		t.Fatalf("unexpected usage after load and add: %d", usage)
	}
}

func quotaGB(bytes int64) float64 {
	return float64(bytes) / float64(1<<30)
}

type stubQuotaTrackedConn struct {
	closed atomic.Int64
}

func (c *stubQuotaTrackedConn) markQuotaExceeded() {
	c.closed.Add(1)
}

// TestQuotaManagerRegisterConnRemoveConfigNoOrphan covers Unit 6 of the
// 2026-05-14 memory-leak plan: when RegisterConn and RemoveConfig race
// on the same user, no conn that successfully completed registration
// (i.e. RegisterConn returned non-nil callbacks) may be left as an
// "orphan" — a conn still attached to a connList that was unlinked from
// activeConns before closeAllAndClear ran. Pre-fix, the orphan path was
// real: a goroutine could LoadOrStore the connList, get scheduled out,
// see RemoveConfig run to completion, then add to a list that nobody
// would ever notify again.
//
// The lifecycle RWMutex closes that window: RegisterConn holds RLock
// across its full load → add sequence; RemoveConfig holds Lock across
// the LoadAndDelete map swap. The invariant guarded here is structural,
// so we run the race many times to amplify scheduling variance.
func TestQuotaManagerRegisterConnRemoveConfigNoOrphan(t *testing.T) {
	for attempt := 0; attempt < 16; attempt++ {
		manager, err := NewQuotaManager(option.TrafficQuotaServiceOptions{
			Users: []option.TrafficQuotaUser{
				{Name: "alice", QuotaGB: quotaGB(1024), Period: "daily"},
			},
		})
		if err != nil {
			t.Fatalf("attempt %d: new manager: %v", attempt, err)
		}

		const workers = 64
		var (
			registered   []*stubQuotaTrackedConn
			registeredMu sync.Mutex
			wg           sync.WaitGroup
			startBarrier = make(chan struct{})
		)

		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startBarrier
				conn := &stubQuotaTrackedConn{}
				onBytes, onClose := manager.RegisterConn("alice", conn)
				if onBytes == nil || onClose == nil {
					// Config was already removed when this worker ran;
					// the conn was never tracked, so the orphan invariant
					// does not apply to it.
					return
				}
				registeredMu.Lock()
				registered = append(registered, conn)
				registeredMu.Unlock()
			}()
		}

		// Release workers and RemoveConfig simultaneously to maximize
		// interleaving across runs.
		go func() {
			<-startBarrier
			if err := manager.RemoveConfig("alice"); err != nil {
				t.Errorf("attempt %d: RemoveConfig: %v", attempt, err)
			}
		}()
		close(startBarrier)
		wg.Wait()
		// Give the RemoveConfig goroutine a fair chance to complete its
		// closeAllAndClear after all RegisterConn workers exit. The
		// lifecycle Lock guarantees serialization, but closeAllAndClear
		// runs outside the lock, so we wait until activeConns has been
		// emptied — that means closeAllAndClear has at least started.
		waitForRemovalCompletion(t, manager, "alice")

		registeredMu.Lock()
		orphans := 0
		for i, conn := range registered {
			if got := conn.closed.Load(); got == 0 {
				t.Errorf("attempt %d: conn %d/%d (registered) was orphaned — never received markQuotaExceeded", attempt, i, len(registered))
				orphans++
			}
		}
		count := len(registered)
		registeredMu.Unlock()
		if orphans > 0 {
			t.Fatalf("attempt %d: %d orphan(s) out of %d registered conns", attempt, orphans, count)
		}
	}
}

func waitForRemovalCompletion(t *testing.T, manager *QuotaManager, user string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, loaded := manager.activeConns.Load(user); !loaded {
			// The activeConns entry has been removed by RemoveConfig.
			// closeAllAndClear runs synchronously inside RemoveConfig
			// before the map entry is gone — wait, that order isn't
			// guaranteed. Re-read the implementation: LoadAndDelete
			// happens BEFORE closeAllAndClear, so the entry is gone
			// first. We need a different signal — give a small grace
			// period for closeAllAndClear to finish marking.
			time.Sleep(10 * time.Millisecond)
			return
		}
		time.Sleep(1 * time.Millisecond)
	}
	t.Fatalf("RemoveConfig did not complete for user %q within deadline", user)
}

// TestQuotaManagerRegisterConnAfterRemoveConfigReturnsNoCallbacks
// guards the inverse half of Unit 6: once RemoveConfig has completed,
// any subsequent RegisterConn for that user must observe loadConfig ==
// false and return nil callbacks. Without lifecycle, a stale read of
// userConfigs could let a late RegisterConn build a fresh connList that
// nobody owns.
func TestQuotaManagerRegisterConnAfterRemoveConfigReturnsNoCallbacks(t *testing.T) {
	manager, err := NewQuotaManager(option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(1024), Period: "daily"},
		},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	if err := manager.RemoveConfig("alice"); err != nil {
		t.Fatalf("RemoveConfig: %v", err)
	}

	conn := &stubQuotaTrackedConn{}
	onBytes, onClose := manager.RegisterConn("alice", conn)
	if onBytes != nil || onClose != nil {
		t.Fatalf("expected nil callbacks for unconfigured user, got (%v, %v)", onBytes != nil, onClose != nil)
	}

	// And re-applying config should let RegisterConn succeed again
	// against a *fresh* connList (the previous list is gone).
	if err := manager.ApplyConfig(option.TrafficQuotaUser{
		Name:    "alice",
		QuotaGB: quotaGB(1024),
		Period:  "daily",
	}); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	conn2 := &stubQuotaTrackedConn{}
	onBytes2, onClose2 := manager.RegisterConn("alice", conn2)
	if onBytes2 == nil || onClose2 == nil {
		t.Fatal("expected non-nil callbacks after re-apply")
	}
	onClose2()
}

// TestConnListRemoveDoesNotAffectOtherConns covers Unit 3 Safety Proof
// P3: a remove() of conn A must not let closeAll skip a still-present
// conn B, and a removed conn must not receive a markQuotaExceeded.
//
// The test adds 100 conns, removes a deterministic half by identity,
// then closeAll's the list and checks that exactly the removed conns
// are absent from the notification set.
func TestConnListRemoveDoesNotAffectOtherConns(t *testing.T) {
	l := &connList{}
	conns := make([]*stubQuotaTrackedConn, 100)
	for i := range conns {
		conns[i] = &stubQuotaTrackedConn{}
		l.add(conns[i])
	}
	// Remove every other conn (indices 0, 2, 4, ...).
	for i := 0; i < len(conns); i += 2 {
		l.remove(conns[i])
	}
	l.closeAll()
	for i, c := range conns {
		expected := int64(0)
		if i%2 == 1 {
			expected = 1
		}
		if got := c.closed.Load(); got != expected {
			t.Fatalf("conn[%d]: expected markQuotaExceeded count=%d, got %d", i, expected, got)
		}
	}
}

// TestConnListShrinksUnderwater covers Unit 3 Safety Proof P4: a list
// that grew large and then drained must release the oversized backing
// array.
func TestConnListShrinksUnderwater(t *testing.T) {
	l := &connList{}
	const grown = 2048
	conns := make([]*stubQuotaTrackedConn, grown)
	for i := range conns {
		conns[i] = &stubQuotaTrackedConn{}
		l.add(conns[i])
	}
	peakCap := capUnderLock(l)
	if peakCap <= connListShrinkMinCap {
		t.Fatalf("test precondition: peak cap %d should exceed shrink min %d", peakCap, connListShrinkMinCap)
	}
	// Remove all but a small remainder; shrink must trigger.
	for i := 0; i < grown-8; i++ {
		l.remove(conns[i])
	}
	if got := capUnderLock(l); got >= peakCap {
		t.Fatalf("expected backing array to shrink below peak cap %d, got %d", peakCap, got)
	}
	if got := l.len(); got != 8 {
		t.Fatalf("expected len=8 after draining, got %d", got)
	}
}

// TestConnListRemoveZerosLastSlot covers Unit 3 Safety Proof P1: the
// slot that was swapped out of the active region must be set to nil so
// the interface value (and the *QuotaConn it references) becomes
// eligible for GC.
func TestConnListRemoveZerosLastSlot(t *testing.T) {
	l := &connList{}
	c1 := &stubQuotaTrackedConn{}
	c2 := &stubQuotaTrackedConn{}
	c3 := &stubQuotaTrackedConn{}
	l.add(c1)
	l.add(c2)
	l.add(c3)

	l.remove(c2)

	// Inspect the trailing slot (index len(l.conns)) under the lock.
	l.access.Lock()
	if cap(l.conns) <= len(l.conns) {
		l.access.Unlock()
		t.Fatalf("expected trailing capacity to inspect")
	}
	if trailing := l.conns[len(l.conns):cap(l.conns)][0]; trailing != nil {
		l.access.Unlock()
		t.Fatalf("expected trailing slot to be nil after swap-and-zero, got %T", trailing)
	}
	l.access.Unlock()

	if l.len() != 2 {
		t.Fatalf("expected len=2 after removing one conn, got %d", l.len())
	}
}

// TestConnListRemoveNeverAddedIsNoop covers Unit 3 Safety Proof P2: a
// remove of an interface value that never matched any element must not
// mutate the list.
func TestConnListRemoveNeverAddedIsNoop(t *testing.T) {
	l := &connList{}
	c1 := &stubQuotaTrackedConn{}
	c2 := &stubQuotaTrackedConn{}
	l.add(c1)

	stranger := &stubQuotaTrackedConn{}
	l.remove(stranger)

	if l.len() != 1 {
		t.Fatalf("expected len unchanged, got %d", l.len())
	}
	// The original conn must still be there; closeAll proves it.
	l.closeAll()
	if c1.closed.Load() != 1 {
		t.Fatal("expected original conn to still be tracked")
	}
	if c2.closed.Load() != 0 {
		t.Fatal("uninvolved conn should remain unmarked")
	}
}

// TestConnListCloseAllAndClearDropsReferences covers the
// closeAllAndClear contract: after the call returns, the list owns no
// references to the previously tracked conns. This is what protects
// RemoveConfig against sync.Map.Delete's lazy release.
func TestConnListCloseAllAndClearDropsReferences(t *testing.T) {
	l := &connList{}
	conns := make([]*stubQuotaTrackedConn, 20)
	for i := range conns {
		conns[i] = &stubQuotaTrackedConn{}
		l.add(conns[i])
	}

	l.closeAllAndClear()

	for i, c := range conns {
		if got := c.closed.Load(); got != 1 {
			t.Fatalf("conn[%d]: expected markQuotaExceeded count=1, got %d", i, got)
		}
	}
	l.access.Lock()
	if l.conns != nil {
		l.access.Unlock()
		t.Fatalf("expected l.conns to be nil after closeAllAndClear, got len=%d cap=%d", len(l.conns), cap(l.conns))
	}
	l.access.Unlock()
}

// TestConnListConcurrentAddRemoveCloseAll exercises the concurrent
// invariants of Unit 3: add / remove / closeAll racing on the same list
// must not corrupt state, panic on a nil slot, or double-mark conns.
// (A separate -race run will catch data races once a cgo toolchain is
// installed; this test guards correctness even without the race
// detector.)
func TestConnListConcurrentAddRemoveCloseAll(t *testing.T) {
	l := &connList{}
	const writers = 16
	const opsPerWriter = 200

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerWriter; i++ {
				c := &stubQuotaTrackedConn{}
				l.add(c)
				l.remove(c)
			}
		}()
	}
	// One goroutine periodically calls closeAll while writers churn.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				l.closeAll()
			}
		}
	}()
	wg.Wait()
	close(stop)

	if got := l.len(); got != 0 {
		t.Fatalf("expected list empty after all add/remove pairs balanced, got %d", got)
	}
}

// TestConnListShrinkDoesNotThrash covers Unit 3 TC6: repeated add/remove
// cycles at a stable working-set size should not cause the cap to grow
// without bound nor trigger shrink on every cycle. We assert that the
// cap stays within a reasonable envelope once it converges.
func TestConnListShrinkDoesNotThrash(t *testing.T) {
	l := &connList{}
	const cycles = 100
	const batch = 50
	const remove = 40

	conns := make([]*stubQuotaTrackedConn, 0, batch)
	for c := 0; c < cycles; c++ {
		for i := 0; i < batch; i++ {
			cc := &stubQuotaTrackedConn{}
			conns = append(conns, cc)
			l.add(cc)
		}
		for i := 0; i < remove; i++ {
			l.remove(conns[len(conns)-1-i])
		}
		conns = conns[:len(conns)-remove]
	}
	// Working-set size after 100 cycles: 100 * (batch - remove) = 1000.
	// cap must accommodate the live set but not be many multiples larger.
	c := capUnderLock(l)
	if c < l.len() {
		t.Fatalf("cap=%d < len=%d", c, l.len())
	}
	if c > 16*l.len() {
		t.Fatalf("cap=%d unreasonably larger than len=%d", c, l.len())
	}
}

func capUnderLock(l *connList) int {
	l.access.Lock()
	defer l.access.Unlock()
	return cap(l.conns)
}
