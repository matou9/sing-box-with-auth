package speedlimiter

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/service/dynamicconfig"
)

func newTestSpeedService(t *testing.T) *Service {
	t.Helper()
	rawService, err := NewService(context.Background(), log.NewNOPFactory().Logger(), "speed", option.SpeedLimiterServiceOptions{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	s, ok := rawService.(*Service)
	if !ok {
		t.Fatalf("unexpected service type: %T", rawService)
	}
	return s
}

func TestSpeedServiceApplyDynamicUpdatesManager(t *testing.T) {
	s := newTestSpeedService(t)

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "bob", UploadMbps: 10, DownloadMbps: 20}); err != nil {
		t.Fatalf("applyDynamic: %v", err)
	}

	config, found := s.GetConfig("bob")
	if !found {
		t.Fatal("expected GetConfig to return config for bob after applyDynamic")
	}
	if config.UploadMbps != 10 {
		t.Errorf("expected UploadMbps=10, got %d", config.UploadMbps)
	}
	if config.DownloadMbps != 20 {
		t.Errorf("expected DownloadMbps=20, got %d", config.DownloadMbps)
	}
}

func TestSpeedServiceRemoveDynamicRemovesFromManager(t *testing.T) {
	s := newTestSpeedService(t)

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "bob", UploadMbps: 10, DownloadMbps: 20}); err != nil {
		t.Fatalf("applyDynamic: %v", err)
	}
	if _, found := s.GetConfig("bob"); !found {
		t.Fatal("expected bob to have config before remove")
	}

	if err := s.removeDynamic("bob"); err != nil {
		t.Fatalf("removeDynamic: %v", err)
	}

	if _, found := s.GetConfig("bob"); found {
		t.Fatal("expected bob config to be removed after removeDynamic")
	}
}

// TestSpeedServiceApplyDynamicZeroSpeeds covers rows delivered by the shared
// dynamic-config sources that carry only quota fields: zero speeds must not be
// treated as an invalid config. An authoritative (full Postgres) row clears an
// existing limit; a partial (Redis) update leaves it untouched.
func TestSpeedServiceApplyDynamicZeroSpeeds(t *testing.T) {
	s := newTestSpeedService(t)

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "bob", QuotaGB: 10, Authoritative: true}); err != nil {
		t.Fatalf("applyDynamic quota-only row for unknown user: %v", err)
	}
	if _, found := s.GetConfig("bob"); found {
		t.Fatal("expected no speed config for bob after quota-only row")
	}

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "bob", UploadMbps: 10, DownloadMbps: 20, Authoritative: true}); err != nil {
		t.Fatalf("applyDynamic: %v", err)
	}

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "bob", QuotaGB: 10}); err != nil {
		t.Fatalf("applyDynamic partial quota-only update: %v", err)
	}
	if _, found := s.GetConfig("bob"); !found {
		t.Fatal("expected partial update to keep bob's speed config")
	}

	if err := s.applyDynamic(dynamicconfig.ConfigRow{User: "bob", QuotaGB: 10, Authoritative: true}); err != nil {
		t.Fatalf("applyDynamic authoritative zero-speed row: %v", err)
	}
	if _, found := s.GetConfig("bob"); found {
		t.Fatal("expected authoritative zero-speed row to remove bob's speed config")
	}
}

// TestSpeedLimiterCloseTerminatesScheduleLoop verifies Unit 1 of the
// 2026-05-14 memory-leak fix plan: Service.Close() must synchronously wait
// for the schedule-loop goroutine to exit. Without the WaitGroup plumbing,
// the loop would outlive Close() and leak across reload cycles.
func TestSpeedLimiterCloseTerminatesScheduleLoop(t *testing.T) {
	// Force prior allocations to release any transient goroutines so the
	// baseline measurement isn't polluted.
	runtime.GC()
	before := runtime.NumGoroutine()

	s := newTestSpeedService(t)
	// Bypass Start() to avoid the Router dependency; the schedule loop is
	// the only goroutine Unit 1 governs and is launched by StartScheduleLoop.
	s.manager.StartScheduleLoop(s.ctx, &s.wg)

	// Give the schedule loop a moment to enter its select.
	time.Sleep(50 * time.Millisecond)

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Close() returning implies wg.Wait() returned, which implies the
	// schedule-loop's `defer wg.Done()` has executed.

	// Allow Go runtime a tick to retire the exited goroutine before reading.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	after := runtime.NumGoroutine()
	if delta := after - before; delta > 2 {
		t.Fatalf("expected goroutine count to return to baseline (tolerance ±2); before=%d after=%d delta=%d", before, after, delta)
	}
}
