package trafficquota

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
)

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
