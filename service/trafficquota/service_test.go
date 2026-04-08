package trafficquota

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
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

type stubPersister struct {
	mu          sync.Mutex
	store       map[string]map[string]int64
	deleteCalls []string
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
	result := make(map[string]int64)
	for user, value := range p.store[periodKey] {
		result[user] = value
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
