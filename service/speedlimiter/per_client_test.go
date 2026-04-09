package speedlimiter

import (
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
)

// --- Unit 1 + 2: per_client config & manager ---

func perClientOptions() option.SpeedLimiterServiceOptions {
	trueVal := true
	falseVal := false
	return option.SpeedLimiterServiceOptions{
		Default: &option.SpeedLimiterDefault{
			UploadMbps:   50,
			DownloadMbps: 100,
			PerClient:    true,
		},
		Groups: []option.SpeedLimiterGroup{
			{Name: "premium", UploadMbps: 100, DownloadMbps: 200, PerClient: &trueVal},
			{Name: "shared", UploadMbps: 10, DownloadMbps: 20, PerClient: &falseVal},
		},
		Users: []option.SpeedLimiterUser{
			{Name: "alice", Group: "premium"},                          // inherits group per_client=true
			{Name: "bob", Group: "shared"},                             // inherits group per_client=false
			{Name: "charlie", UploadMbps: 5, DownloadMbps: 10},        // inherits default per_client=true
			{Name: "dave", Group: "premium", PerClient: &falseVal},     // user override per_client=false
			{Name: "eve", Group: "shared", PerClient: &trueVal},        // user override per_client=true
		},
	}
}

var (
	addrA = netip.MustParseAddr("1.2.3.4")
	addrB = netip.MustParseAddr("5.6.7.8")
	addrV6 = netip.MustParseAddr("2001:db8::1")
)

func TestPerClient_DifferentSourceAddr_DifferentLimiter(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	// alice: group premium, per_client=true
	ul1 := m.GetOrCreateLimiterForClient("alice", addrA)
	ul2 := m.GetOrCreateLimiterForClient("alice", addrB)
	if ul1 == nil || ul2 == nil {
		t.Fatal("expected non-nil limiters")
	}
	if ul1 == ul2 {
		t.Error("per_client=true: expected different limiter instances for different source IPs")
	}
	// Same source IP → same instance
	ul3 := m.GetOrCreateLimiterForClient("alice", addrA)
	if ul1 != ul3 {
		t.Error("per_client=true: expected same limiter instance for same source IP")
	}
}

func TestPerClient_False_SameSourceAddr_SameLimiter(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	// bob: group shared, per_client=false
	ul1 := m.GetOrCreateLimiterForClient("bob", addrA)
	ul2 := m.GetOrCreateLimiterForClient("bob", addrB)
	if ul1 == nil || ul2 == nil {
		t.Fatal("expected non-nil limiters")
	}
	if ul1 != ul2 {
		t.Error("per_client=false: expected same limiter instance regardless of source IP")
	}
}

func TestPerClient_UserOverride_False_OverridesGroupTrue(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	// dave: group premium (per_client=true), user override per_client=false
	ul1 := m.GetOrCreateLimiterForClient("dave", addrA)
	ul2 := m.GetOrCreateLimiterForClient("dave", addrB)
	if ul1 == nil || ul2 == nil {
		t.Fatal("expected non-nil limiters")
	}
	if ul1 != ul2 {
		t.Error("user per_client=false should override group per_client=true")
	}
}

func TestPerClient_UserOverride_True_OverridesGroupFalse(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	// eve: group shared (per_client=false), user override per_client=true
	ul1 := m.GetOrCreateLimiterForClient("eve", addrA)
	ul2 := m.GetOrCreateLimiterForClient("eve", addrB)
	if ul1 == nil || ul2 == nil {
		t.Fatal("expected non-nil limiters")
	}
	if ul1 == ul2 {
		t.Error("user per_client=true should override group per_client=false")
	}
}

func TestPerClient_NilFallbackToDefault(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	// charlie: no group, no user per_client override → fallback to default per_client=true
	ul1 := m.GetOrCreateLimiterForClient("charlie", addrA)
	ul2 := m.GetOrCreateLimiterForClient("charlie", addrB)
	if ul1 == nil || ul2 == nil {
		t.Fatal("expected non-nil limiters")
	}
	if ul1 == ul2 {
		t.Error("default per_client=true: expected different limiter instances")
	}
}

func TestPerClient_IPv6_DifferentLimiter(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	// alice: per_client=true, test IPv6 source addresses
	ul1 := m.GetOrCreateLimiterForClient("alice", addrA)
	ul2 := m.GetOrCreateLimiterForClient("alice", addrV6)
	if ul1 == nil || ul2 == nil {
		t.Fatal("expected non-nil limiters")
	}
	if ul1 == ul2 {
		t.Error("per_client=true: IPv4 and IPv6 should get different limiters")
	}
	// Same IPv6 → same instance
	ul3 := m.GetOrCreateLimiterForClient("alice", addrV6)
	if ul2 != ul3 {
		t.Error("same IPv6 source should return same limiter")
	}
}

func TestPerClient_RatesCorrect(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	// alice: premium group → 100/200
	ul := m.GetOrCreateLimiterForClient("alice", addrA)
	if ul == nil {
		t.Fatal("expected limiter")
	}
	assertRate(t, "alice per-client upload", ul.Upload, 100)
	assertRate(t, "alice per-client download", ul.Download, 200)
}

func TestPerClient_ScheduleUpdatesAllClientLimiters(t *testing.T) {
	opts := perClientOptions()
	opts.Schedules = []option.SpeedLimiterSchedule{
		{TimeRange: "18:00-23:00", UploadMbps: 30, DownloadMbps: 60},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	// Create client limiters for alice from two IPs
	ul1 := m.GetOrCreateLimiterForClient("alice", addrA)
	ul2 := m.GetOrCreateLimiterForClient("alice", addrB)
	if ul1 == ul2 {
		t.Fatal("expected different limiter instances")
	}

	// Apply schedule
	m.CheckSchedules(timeAt(19, 0))

	// Both should be updated (alice has no fixed per-user override)
	assertRate(t, "alice client1 upload after schedule", ul1.Upload, 30)
	assertRate(t, "alice client1 download after schedule", ul1.Download, 60)
	assertRate(t, "alice client2 upload after schedule", ul2.Upload, 30)
	assertRate(t, "alice client2 download after schedule", ul2.Download, 60)
}

func TestPerClient_RemoveConfig_CleansAllClientLimiters(t *testing.T) {
	// Use options with no default so RemoveConfig actually removes all config
	trueVal := true
	opts := option.SpeedLimiterServiceOptions{
		Groups: []option.SpeedLimiterGroup{
			{Name: "premium", UploadMbps: 100, DownloadMbps: 200, PerClient: &trueVal},
		},
		Users: []option.SpeedLimiterUser{
			{Name: "alice", Group: "premium"},
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	// Create client limiters for alice
	ul1 := m.GetOrCreateLimiterForClient("alice", addrA)
	ul2 := m.GetOrCreateLimiterForClient("alice", addrB)
	if ul1 == nil || ul2 == nil {
		t.Fatal("expected non-nil limiters")
	}
	if ul1 == ul2 {
		t.Fatal("expected different limiter instances")
	}

	// Remove alice's config
	if err := m.RemoveConfig("alice"); err != nil {
		t.Fatal(err)
	}

	// New lookups should return nil (no config, no default)
	ul3 := m.GetOrCreateLimiterForClient("alice", addrA)
	if ul3 != nil {
		t.Error("expected nil limiter after RemoveConfig with no default")
	}
}

func TestPerClient_ConcurrentGetOrCreate(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make([]*UserLimiter, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = m.GetOrCreateLimiterForClient("alice", addrA)
		}(i)
	}
	wg.Wait()

	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Fatalf("goroutine %d got different limiter instance for same user+addr", i)
		}
	}
}

// --- Unit 4: TTL cleanup ---

func TestPerClient_TTL_CleansExpiredClientLimiters(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	now := timeAt(10, 0)
	m.now = func() time.Time { return now }

	// Create client limiters
	ul := m.GetOrCreateLimiterForClient("alice", addrA)
	if ul == nil {
		t.Fatal("expected limiter")
	}

	// Advance time past TTL (default 10 min)
	now = timeAt(10, 11)
	m.now = func() time.Time { return now }
	m.CheckSchedules(now)

	// Client limiter should be cleaned up
	ul2 := m.GetOrCreateLimiterForClient("alice", addrA)
	if ul2 == nil {
		t.Fatal("expected new limiter after TTL cleanup")
	}
	if ul == ul2 {
		t.Error("expected different limiter instance after TTL cleanup (old one should have been removed)")
	}
}

func TestPerClient_TTL_ActiveLimiterNotCleaned(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	now := timeAt(10, 0)
	m.now = func() time.Time { return now }

	ul1 := m.GetOrCreateLimiterForClient("alice", addrA)
	if ul1 == nil {
		t.Fatal("expected limiter")
	}

	// Advance 5 minutes, touch again
	now = timeAt(10, 5)
	m.now = func() time.Time { return now }
	ul2 := m.GetOrCreateLimiterForClient("alice", addrA)

	// Advance another 6 minutes (11 min from start, but only 6 min from last touch)
	now = timeAt(10, 11)
	m.now = func() time.Time { return now }
	m.CheckSchedules(now)

	// Should still be the same instance (was touched 6 min ago, within TTL)
	ul3 := m.GetOrCreateLimiterForClient("alice", addrA)
	if ul1 != ul2 || ul2 != ul3 {
		t.Error("active limiter should not be cleaned up within TTL of last touch")
	}
}

func TestPerClient_TTL_PerUserLimiterNeverCleaned(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	now := timeAt(10, 0)
	m.now = func() time.Time { return now }

	// bob: per_client=false → per-user limiter
	ul1 := m.GetOrCreateLimiterForClient("bob", addrA)
	if ul1 == nil {
		t.Fatal("expected limiter")
	}

	// Advance past TTL
	now = timeAt(10, 20)
	m.now = func() time.Time { return now }
	m.CheckSchedules(now)

	// Per-user limiter should NOT be cleaned
	ul2 := m.GetOrCreateLimiterForClient("bob", addrA)
	if ul1 != ul2 {
		t.Error("per-user limiter (no | in key) should never be TTL-cleaned")
	}
}

func TestPerClient_TTL_BoundaryNotCleaned(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	now := timeAt(10, 0)
	m.now = func() time.Time { return now }

	ul1 := m.GetOrCreateLimiterForClient("alice", addrA)
	if ul1 == nil {
		t.Fatal("expected limiter")
	}

	// Advance exactly to TTL boundary (10 min)
	now = timeAt(10, 10)
	m.now = func() time.Time { return now }
	m.CheckSchedules(now)

	// At exact boundary, should NOT be cleaned (> not >=)
	ul2 := m.GetOrCreateLimiterForClient("alice", addrA)
	if ul1 != ul2 {
		t.Error("limiter at exact TTL boundary should not be cleaned")
	}
}

// --- Unit 2: ApplyConfig dynamic per_client changes ---

func TestPerClient_ApplyConfig_EnablePerClient(t *testing.T) {
	// Start with per_client=false (no default per_client)
	opts := option.SpeedLimiterServiceOptions{
		Default: &option.SpeedLimiterDefault{
			UploadMbps:   50,
			DownloadMbps: 100,
		},
		Users: []option.SpeedLimiterUser{
			{Name: "alice", UploadMbps: 10, DownloadMbps: 20},
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	// Initially per_client=false: same limiter for different IPs
	ul1 := m.GetOrCreateLimiterForClient("alice", addrA)
	ul2 := m.GetOrCreateLimiterForClient("alice", addrB)
	if ul1 != ul2 {
		t.Fatal("expected same limiter before enabling per_client")
	}

	// Dynamically enable per_client
	trueVal := true
	if err := m.ApplyConfig(option.SpeedLimiterUser{
		Name:         "alice",
		UploadMbps:   10,
		DownloadMbps: 20,
		PerClient:    &trueVal,
	}); err != nil {
		t.Fatal(err)
	}

	// Now different IPs should get different limiters
	ul3 := m.GetOrCreateLimiterForClient("alice", addrA)
	ul4 := m.GetOrCreateLimiterForClient("alice", addrB)
	if ul3 == ul4 {
		t.Error("expected different limiters after enabling per_client")
	}
}

func TestPerClient_ApplyConfig_DisablePerClient(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	// alice: per_client=true initially
	ul1 := m.GetOrCreateLimiterForClient("alice", addrA)
	ul2 := m.GetOrCreateLimiterForClient("alice", addrB)
	if ul1 == ul2 {
		t.Fatal("expected different limiters with per_client=true")
	}

	// Dynamically disable per_client
	falseVal := false
	if err := m.ApplyConfig(option.SpeedLimiterUser{
		Name:         "alice",
		Group:        "premium",
		PerClient:    &falseVal,
	}); err != nil {
		t.Fatal(err)
	}

	// Now different IPs should get the same limiter
	ul3 := m.GetOrCreateLimiterForClient("alice", addrA)
	ul4 := m.GetOrCreateLimiterForClient("alice", addrB)
	if ul3 != ul4 {
		t.Error("expected same limiter after disabling per_client")
	}
}

func TestPerClient_ApplyConfig_RatesPreserved(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	// Apply new speed with per_client
	trueVal := true
	if err := m.ApplyConfig(option.SpeedLimiterUser{
		Name:         "newuser",
		UploadMbps:   25,
		DownloadMbps: 50,
		PerClient:    &trueVal,
	}); err != nil {
		t.Fatal(err)
	}

	ul := m.GetOrCreateLimiterForClient("newuser", addrA)
	if ul == nil {
		t.Fatal("expected limiter for newuser")
	}
	assertRate(t, "newuser upload", ul.Upload, 25)
	assertRate(t, "newuser download", ul.Download, 50)

	// Different IP should have same rates but different instance
	ul2 := m.GetOrCreateLimiterForClient("newuser", addrB)
	if ul == ul2 {
		t.Error("expected different limiter instances for per_client user")
	}
	assertRate(t, "newuser addr2 upload", ul2.Upload, 25)
	assertRate(t, "newuser addr2 download", ul2.Download, 50)
}

// --- Unit 2: backward compat - GetOrCreateLimiter still works ---

func TestPerClient_BackwardCompat_GetOrCreateLimiter(t *testing.T) {
	m, err := NewLimiterManager(perClientOptions())
	if err != nil {
		t.Fatal(err)
	}

	// Old API should still work
	ul := m.GetOrCreateLimiter("bob")
	if ul == nil {
		t.Fatal("expected limiter via old API")
	}
	assertRate(t, "bob upload via old API", ul.Upload, 10)
	assertRate(t, "bob download via old API", ul.Download, 20)
}
