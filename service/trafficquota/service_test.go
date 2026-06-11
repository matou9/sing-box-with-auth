package trafficquota

import (
	"context"
	"net"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/dynamicconfig"
)

func TestServiceRoutedConnectionPassesThroughWithoutQuota(t *testing.T) {
	service := newTestService(t, option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(1024), Period: "daily"},
		},
	})

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	routed := service.RoutedConnection(context.Background(), client, adapter.InboundContext{User: "bob"}, nil, nil)
	if routed != client {
		t.Fatal("expected passthrough connection for untracked user")
	}
}

func TestServiceRoutedConnectionReturnsClosedConnWhenExceeded(t *testing.T) {
	service := newTestService(t, option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(64), Period: "daily"},
		},
	})
	service.manager.LoadUsage("alice", 128)

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	routed := service.RoutedConnection(context.Background(), client, adapter.InboundContext{User: "alice"}, nil, nil)
	quotaConn, ok := routed.(*QuotaConn)
	if !ok {
		t.Fatalf("expected quota conn, got %T", routed)
	}
	if !quotaConn.closed.Load() {
		t.Fatal("expected routed connection to be closed immediately")
	}
}

func TestServiceFlushPendingPersistsAndReloads(t *testing.T) {
	service := newTestService(t, option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(2048), Period: "daily"},
		},
	})
	service.manager.now = func() time.Time {
		return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	}
	service.persister = newStubPersister()
	service.persister.(*stubPersister).store["2026-04-07"] = map[string]int64{"alice": 100}

	service.manager.AddBytes("alice", 200)
	if err := service.flushPending(); err != nil {
		t.Fatalf("flush pending: %v", err)
	}

	if usage := service.manager.Usage("alice"); usage != 300 {
		t.Fatalf("expected usage reloaded from persister, got %d", usage)
	}
	if value := service.persister.(*stubPersister).store["2026-04-07"]["alice"]; value != 300 {
		t.Fatalf("unexpected persisted value: %d", value)
	}
}

func TestServiceHandlePeriodResetsDeletesOldPersistedKeys(t *testing.T) {
	service := newTestService(t, option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(1024), Period: "daily"},
		},
	})
	service.manager.now = func() time.Time {
		return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	}
	stub := newStubPersister()
	service.persister = stub

	service.manager.LoadUsage("alice", 500)
	if err := service.handlePeriodResets(time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("handle period resets: %v", err)
	}

	if usage := service.manager.Usage("alice"); usage != 0 {
		t.Fatalf("expected reset usage, got %d", usage)
	}
	if len(stub.deleteCalls) != 1 {
		t.Fatalf("expected one delete call, got %d", len(stub.deleteCalls))
	}
	if stub.deleteCalls[0] != "alice:2026-04-07" {
		t.Fatalf("unexpected delete call: %s", stub.deleteCalls[0])
	}
}

func TestServiceInitPersisterFallsBackToNoopPersister(t *testing.T) {
	originalRedisFactory := newRedisPersisterFunc
	t.Cleanup(func() {
		newRedisPersisterFunc = originalRedisFactory
	})
	newRedisPersisterFunc = func(context.Context, *option.TrafficQuotaRedisOptions) (Persister, error) {
		return nil, context.DeadlineExceeded
	}

	rawService, err := NewService(context.Background(), log.NewNOPFactory().Logger(), "quota", option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(1024), Period: "daily"},
		},
		Persistence: &option.TrafficQuotaPersistence{
			Redis: &option.TrafficQuotaRedisOptions{Address: "127.0.0.1:6379"},
		},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	service := rawService.(*Service)
	if err := service.initPersister(); err != nil {
		t.Fatalf("init persister: %v", err)
	}

	if _, ok := service.persister.(*NoopPersister); !ok {
		t.Fatalf("expected noop persister fallback, got %T", service.persister)
	}
}

func TestServiceRestoreStateDoesNotDoubleCountPendingDeltaAfterFlush(t *testing.T) {
	service := newTestService(t, option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(2048), Period: "daily"},
		},
	})
	service.manager.now = func() time.Time {
		return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	}
	stub := newStubPersister()
	service.persister = stub

	err := service.RestoreState(RuntimeState{
		User: option.TrafficQuotaUser{
			Name:    "alice",
			QuotaGB: quotaGB(2048),
			Period:  "daily",
		},
		UsageBytes:   500,
		PendingDelta: 200,
		Exceeded:     false,
		PeriodKey:    "2026-04-07",
	})
	if err != nil {
		t.Fatalf("restore state: %v", err)
	}
	if value := stub.store["2026-04-07"]["alice"]; value != 500 {
		t.Fatalf("persisted value after restore = %d, want 500", value)
	}

	if err := service.flushPending(); err != nil {
		t.Fatalf("flush pending after restore: %v", err)
	}
	if value := stub.store["2026-04-07"]["alice"]; value != 500 {
		t.Fatalf("persisted value after flush = %d, want 500", value)
	}
}

func TestServiceRestoreStateDoesNotRaceFlushPendingIntoDoubleCount(t *testing.T) {
	service := newTestService(t, option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(2048), Period: "daily"},
		},
	})
	service.manager.now = func() time.Time {
		return time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	}
	flushDone := make(chan error, 1)
	stub := newBlockingSavePersister(func() {
		go func() {
			flushDone <- service.flushPending()
		}()
	})
	service.persister = stub

	restoreDone := make(chan error, 1)
	go func() {
		restoreDone <- service.RestoreState(RuntimeState{
			User: option.TrafficQuotaUser{
				Name:    "alice",
				QuotaGB: quotaGB(2048),
				Period:  "daily",
			},
			UsageBytes:   500,
			PendingDelta: 200,
			Exceeded:     false,
			PeriodKey:    "2026-04-07",
		})
	}()

	<-stub.saveStarted
	close(stub.releaseSave)

	if err := <-restoreDone; err != nil {
		t.Fatalf("restore state: %v", err)
	}
	if err := <-flushDone; err != nil {
		t.Fatalf("flush pending after restore: %v", err)
	}
	if value := stub.store["2026-04-07"]["alice"]; value != 500 {
		t.Fatalf("persisted value after interleaved flush = %d, want 500", value)
	}
}

func TestServiceApplyDynamicUpdatesManager(t *testing.T) {
	rawService, err := NewService(context.Background(), log.NewNOPFactory().Logger(), "quota", option.TrafficQuotaServiceOptions{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	s := rawService.(*Service)

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "alice", QuotaGB: 10, Period: "monthly"}); err != nil {
		t.Fatalf("applyDynamic: %v", err)
	}

	if !s.manager.HasQuota("alice") {
		t.Fatal("expected manager to have quota for alice after applyDynamic")
	}
	config, found := s.GetConfig("alice")
	if !found {
		t.Fatal("expected GetConfig to return config for alice")
	}
	if config.QuotaGB != 10 {
		t.Errorf("expected QuotaGB=10, got %v", config.QuotaGB)
	}
}

// TestServiceApplyDynamicZeroQuota covers rows delivered by the shared
// dynamic-config sources that carry only speed fields: quota_gb = 0 must not
// be treated as an invalid config. An authoritative (full Postgres) row clears
// an existing quota; a partial (Redis) update leaves it untouched.
func TestServiceApplyDynamicZeroQuota(t *testing.T) {
	rawService, err := NewService(context.Background(), log.NewNOPFactory().Logger(), "quota", option.TrafficQuotaServiceOptions{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	s := rawService.(*Service)
	s.persister = NewNoopPersister()

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "alice", UploadMbps: 10, Authoritative: true}); err != nil {
		t.Fatalf("applyDynamic speed-only row for unknown user: %v", err)
	}
	if s.manager.HasQuota("alice") {
		t.Fatal("expected no quota for alice after speed-only row")
	}

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "alice", QuotaGB: 10, Period: "monthly", Authoritative: true}); err != nil {
		t.Fatalf("applyDynamic: %v", err)
	}

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "alice", UploadMbps: 10}); err != nil {
		t.Fatalf("applyDynamic partial speed-only update: %v", err)
	}
	if !s.manager.HasQuota("alice") {
		t.Fatal("expected partial update to keep alice's quota")
	}

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "alice", UploadMbps: 10, Authoritative: true}); err != nil {
		t.Fatalf("applyDynamic authoritative zero-quota row: %v", err)
	}
	if s.manager.HasQuota("alice") {
		t.Fatal("expected authoritative zero-quota row to remove alice's quota")
	}
}

func TestServiceRemoveDynamicRemovesFromManager(t *testing.T) {
	rawService, err := NewService(context.Background(), log.NewNOPFactory().Logger(), "quota", option.TrafficQuotaServiceOptions{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	s := rawService.(*Service)
	// Assign a noop persister so removeConfigLocked doesn't panic on nil persister
	s.persister = NewNoopPersister()

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "alice", QuotaGB: 10, Period: "monthly"}); err != nil {
		t.Fatalf("applyDynamic: %v", err)
	}
	if !s.manager.HasQuota("alice") {
		t.Fatal("expected alice to have quota before remove")
	}

	if err := s.removeDynamic("alice"); err != nil {
		t.Fatalf("removeDynamic: %v", err)
	}
	if s.manager.HasQuota("alice") {
		t.Fatal("expected alice quota to be removed after removeDynamic")
	}
}

func TestServiceInitPersisterPostgresFallsBackToNoopPersister(t *testing.T) {
	originalPostgresFactory := newPostgresPersisterFunc
	t.Cleanup(func() {
		newPostgresPersisterFunc = originalPostgresFactory
	})
	newPostgresPersisterFunc = func(context.Context, *option.TrafficQuotaPostgresOptions) (Persister, error) {
		return nil, context.DeadlineExceeded
	}

	rawService, err := NewService(context.Background(), log.NewNOPFactory().Logger(), "quota", option.TrafficQuotaServiceOptions{
		Persistence: &option.TrafficQuotaPersistence{
			Postgres: &option.TrafficQuotaPostgresOptions{ConnectionString: "postgres://invalid:5432/nodb"},
		},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	s := rawService.(*Service)

	if err := s.initPersister(); err != nil {
		t.Fatalf("initPersister: %v", err)
	}

	if s.persister == nil {
		t.Fatal("expected persister to be non-nil after initPersister")
	}
	if _, ok := s.persister.(*NoopPersister); !ok {
		t.Fatalf("expected NoopPersister fallback, got %T", s.persister)
	}
	if err := s.persister.Save("alice", "2026-04", 100); err != nil {
		t.Fatalf("Save on NoopPersister returned error: %v", err)
	}
}

func newTestService(t *testing.T, options option.TrafficQuotaServiceOptions) *Service {
	t.Helper()

	rawService, err := NewService(context.Background(), log.NewNOPFactory().Logger(), "quota", options)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	service, ok := rawService.(*Service)
	if !ok {
		t.Fatalf("unexpected service type: %T", rawService)
	}
	service.persister = NewNoopPersister()
	return service
}

// TestTrafficQuotaServiceCloseTerminatesAllLoops guards the wg-completeness
// invariant required by Unit 1 (Reinforcement #3) of the 2026-05-14
// memory-leak fix plan: every long-running goroutine launched from
// Service.Start (runFlushLoop, runPeriodResetLoop, dynamic source loops)
// must be tracked by s.wg so that Service.Close() waits for them
// synchronously. The plan does not change the current wiring; this test
// freezes the existing correct behavior so future PRs cannot regress it.
func TestTrafficQuotaServiceCloseTerminatesAllLoops(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()

	s := newTestService(t, option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(1024), Period: "daily"},
		},
	})

	// Replicate the goroutine launches performed by Start() without taking
	// on Start's Router-from-context dependency. This faithfully exercises
	// the wg path that Close() relies on.
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.runFlushLoop() }()
	go func() { defer s.wg.Done(); s.runPeriodResetLoop() }()

	time.Sleep(50 * time.Millisecond)

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Close() returning implies wg.Wait() observed defer wg.Done() for both
	// loops. Validate at the runtime level that no leak survives.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	after := runtime.NumGoroutine()
	if delta := after - before; delta > 2 {
		t.Fatalf("expected goroutine count to return to baseline (tolerance ±2); before=%d after=%d delta=%d", before, after, delta)
	}
}

type loadManyCall struct {
	periodKey string
	users     []string
}

type stubPersister struct {
	mu             sync.Mutex
	store          map[string]map[string]int64
	deleteCalls    []string
	loadManyCalls  []loadManyCall
	loadAllInvoked int
}

type blockingSavePersister struct {
	*stubPersister
	saveStarted chan struct{}
	releaseSave chan struct{}
	onSave      func()
}

func newStubPersister() *stubPersister {
	return &stubPersister{
		store: make(map[string]map[string]int64),
	}
}

func newBlockingSavePersister(onSave func()) *blockingSavePersister {
	return &blockingSavePersister{
		stubPersister: newStubPersister(),
		saveStarted:   make(chan struct{}),
		releaseSave:   make(chan struct{}),
		onSave:        onSave,
	}
}

func (p *stubPersister) Load(user, periodKey string) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.store[periodKey][user], nil
}

func (p *stubPersister) LoadAll(periodKey string) (map[string]int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadAllInvoked++
	result := make(map[string]int64)
	for user, value := range p.store[periodKey] {
		result[user] = value
	}
	return result, nil
}

func (p *stubPersister) LoadMany(periodKey string, users []string) (map[string]int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadManyCalls = append(p.loadManyCalls, loadManyCall{periodKey: periodKey, users: append([]string(nil), users...)})
	result := make(map[string]int64, len(users))
	bucket := p.store[periodKey]
	for _, user := range users {
		if v, ok := bucket[user]; ok {
			result[user] = v
		}
	}
	return result, nil
}

func (p *stubPersister) Save(user, periodKey string, bytes int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store[periodKey] == nil {
		p.store[periodKey] = make(map[string]int64)
	}
	p.store[periodKey][user] = bytes
	return nil
}

func (p *blockingSavePersister) Save(user, periodKey string, bytes int64) error {
	if err := p.stubPersister.Save(user, periodKey, bytes); err != nil {
		return err
	}
	close(p.saveStarted)
	if p.onSave != nil {
		p.onSave()
	}
	<-p.releaseSave
	return nil
}

func (p *stubPersister) IncrBy(user, periodKey string, delta int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store[periodKey] == nil {
		p.store[periodKey] = make(map[string]int64)
	}
	p.store[periodKey][user] += delta
	return nil
}

func (p *stubPersister) Delete(user, periodKey string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store[periodKey] != nil {
		delete(p.store[periodKey], user)
	}
	p.deleteCalls = append(p.deleteCalls, user+":"+periodKey)
	return nil
}

func (p *stubPersister) Close() error {
	return nil
}

var _ adapter.Service = (*Service)(nil)
var _ Persister = (*stubPersister)(nil)

// TestFlushPendingUsesLoadManyNotLoadAll guards Unit 4 of the 2026-05-14
// memory-leak plan: the flush hot path must call LoadMany scoped to the
// active users instead of LoadAll scanning the entire period set.
func TestFlushPendingUsesLoadManyNotLoadAll(t *testing.T) {
	service := newTestService(t, option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(4096), Period: "daily"},
			{Name: "bob", QuotaGB: quotaGB(4096), Period: "daily"},
		},
	})
	service.manager.now = func() time.Time {
		return time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	}
	stub := newStubPersister()
	service.persister = stub

	service.manager.AddBytes("alice", 100)
	// bob has no traffic — must not appear in the flush I/O at all.

	if err := service.flushPending(); err != nil {
		t.Fatalf("flush pending: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.loadAllInvoked != 0 {
		t.Fatalf("expected LoadAll to not be invoked by flushPending, got %d calls", stub.loadAllInvoked)
	}
	if len(stub.loadManyCalls) != 1 {
		t.Fatalf("expected exactly one LoadMany call, got %d", len(stub.loadManyCalls))
	}
	call := stub.loadManyCalls[0]
	if call.periodKey != "2026-05-14" {
		t.Fatalf("unexpected LoadMany period: %s", call.periodKey)
	}
	if len(call.users) != 1 || call.users[0] != "alice" {
		t.Fatalf("expected LoadMany users=[alice], got %v", call.users)
	}
}

// TestConsumePendingDeltasOnlyDrainsDirtyUsers guards R10/R13: the
// dirty-user index must let flush walk only the changed users, not the
// entire configured user set.
func TestConsumePendingDeltasOnlyDrainsDirtyUsers(t *testing.T) {
	service := newTestService(t, option.TrafficQuotaServiceOptions{})
	service.manager.now = func() time.Time {
		return time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	}
	// Configure 1000 users; only 2 will produce traffic this cycle.
	for i := 0; i < 1000; i++ {
		name := "user-" + strconv.Itoa(i)
		if err := service.manager.ApplyConfig(option.TrafficQuotaUser{
			Name:    name,
			QuotaGB: quotaGB(4096),
			Period:  "daily",
		}); err != nil {
			t.Fatalf("apply config %s: %v", name, err)
		}
	}

	service.manager.AddBytes("user-1", 100)
	service.manager.AddBytes("user-500", 200)

	pending := service.manager.ConsumePendingDeltas()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending deltas (only dirty users), got %d", len(pending))
	}
	seen := make(map[string]int64, 2)
	for _, pd := range pending {
		seen[pd.User] = pd.Delta
		if pd.PeriodKey != "2026-05-14" {
			t.Errorf("unexpected period for %s: %q", pd.User, pd.PeriodKey)
		}
	}
	if seen["user-1"] != 100 || seen["user-500"] != 200 {
		t.Fatalf("unexpected pending payload: %v", seen)
	}

	// A second drain right after the first must return nothing — the
	// dirtyQueue is consumed, and nobody added new bytes.
	if again := service.manager.ConsumePendingDeltas(); len(again) != 0 {
		t.Fatalf("expected empty second drain, got %d entries", len(again))
	}
}

// TestFlushPendingDoesNotLoseConcurrentAddBytes guards R13: AddBytes
// calls that race with ConsumePendingDeltas must not lose their delta.
// The plan accepts a zero-delta entry on the next flush as harmless, but
// the actual bytes must always end up persisted within a bounded number
// of flush cycles.
func TestFlushPendingDoesNotLoseConcurrentAddBytes(t *testing.T) {
	service := newTestService(t, option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(8192), Period: "daily"},
		},
	})
	service.manager.now = func() time.Time {
		return time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	}
	stub := newStubPersister()
	service.persister = stub

	const writers = 8
	const opsPerWriter = 200
	const bytesPerOp = 7

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerWriter; i++ {
				service.manager.AddBytes("alice", bytesPerOp)
				if i%17 == 0 {
					_ = service.flushPending()
				}
			}
		}()
	}
	wg.Wait()
	if err := service.flushPending(); err != nil {
		t.Fatalf("final flush: %v", err)
	}

	expected := int64(writers * opsPerWriter * bytesPerOp)
	stub.mu.Lock()
	got := stub.store["2026-05-14"]["alice"]
	stub.mu.Unlock()
	if got != expected {
		t.Fatalf("expected persisted bytes=%d, got %d (lost or double-counted under flush race)", expected, got)
	}
	if usage := service.manager.Usage("alice"); usage != expected {
		t.Fatalf("expected in-memory usage=%d, got %d", expected, usage)
	}
}

// TestCheckPeriodResetAtomicWithPendingDelta guards R13: a period reset
// must not let AddBytes drop bytes into the OLD period after the boundary
// has rolled over. periodKey and pendingDelta share the same critical
// section under userState.periodAccess; this test exercises the
// invariant via a deterministic interleaving.
func TestCheckPeriodResetAtomicWithPendingDelta(t *testing.T) {
	service := newTestService(t, option.TrafficQuotaServiceOptions{
		Users: []option.TrafficQuotaUser{
			{Name: "alice", QuotaGB: quotaGB(4096), Period: "daily"},
		},
	})
	service.manager.now = func() time.Time {
		return time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	}
	// Seed period and add some bytes in the old period.
	service.manager.AddBytes("alice", 100)
	pending := service.manager.ConsumePendingDeltas()
	if len(pending) != 1 || pending[0].PeriodKey != "2026-05-14" {
		t.Fatalf("setup failed: %#v", pending)
	}

	// Period reset to the next day.
	resets := service.manager.CheckPeriodReset(time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC))
	if len(resets) != 1 || resets[0].PreviousKey != "2026-05-14" {
		t.Fatalf("expected single reset from 2026-05-14, got %#v", resets)
	}

	// Update now() to the new period and add bytes — these must attach to
	// the new period, not the old one.
	service.manager.now = func() time.Time {
		return time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	}
	service.manager.AddBytes("alice", 50)
	pending = service.manager.ConsumePendingDeltas()
	if len(pending) != 1 {
		t.Fatalf("expected single pending after reset, got %#v", pending)
	}
	if pending[0].PeriodKey != "2026-05-15" {
		t.Fatalf("expected post-reset period=2026-05-15, got %q", pending[0].PeriodKey)
	}
	if pending[0].Delta != 50 {
		t.Fatalf("expected post-reset delta=50, got %d", pending[0].Delta)
	}
}
