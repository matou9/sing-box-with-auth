package speedlimiter

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"

	"golang.org/x/time/rate"
)

func baseOptions() option.SpeedLimiterServiceOptions {
	return option.SpeedLimiterServiceOptions{
		Default: &option.SpeedLimiterDefault{
			UploadMbps:   50,
			DownloadMbps: 100,
		},
		Groups: []option.SpeedLimiterGroup{
			{Name: "premium", UploadMbps: 100, DownloadMbps: 200},
			{Name: "basic", UploadMbps: 10, DownloadMbps: 20},
		},
		Users: []option.SpeedLimiterUser{
			{Name: "alice", Group: "premium"},
			{Name: "bob", Group: "basic"},
			{Name: "charlie", UploadMbps: 5, DownloadMbps: 10},
			{Name: "dave", Group: "premium", UploadMbps: 30}, // partial override: upload only
		},
	}
}

func TestManager_UserOverride(t *testing.T) {
	m, err := NewLimiterManager(baseOptions())
	if err != nil {
		t.Fatal(err)
	}

	ul := m.GetOrCreateLimiter("charlie")
	if ul == nil {
		t.Fatal("expected limiter for charlie")
	}
	// charlie: upload=5Mbps, download=10Mbps
	assertRate(t, "charlie upload", ul.Upload, 5)
	assertRate(t, "charlie download", ul.Download, 10)
}

func TestManager_GroupConfig(t *testing.T) {
	m, err := NewLimiterManager(baseOptions())
	if err != nil {
		t.Fatal(err)
	}

	ul := m.GetOrCreateLimiter("alice")
	if ul == nil {
		t.Fatal("expected limiter for alice")
	}
	// alice: group premium → upload=100, download=200
	assertRate(t, "alice upload", ul.Upload, 100)
	assertRate(t, "alice download", ul.Download, 200)
}

func TestManager_DefaultConfig(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Default: &option.SpeedLimiterDefault{
			UploadMbps:   50,
			DownloadMbps: 100,
		},
		Users: []option.SpeedLimiterUser{
			{Name: "unknown_user"},
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	// User in list but no group or override → default
	ul := m.GetOrCreateLimiter("unknown_user")
	if ul == nil {
		t.Fatal("expected default limiter")
	}
	assertRate(t, "default upload", ul.Upload, 50)
	assertRate(t, "default download", ul.Download, 100)
}

func TestManager_UnknownUser_NoDefault(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Groups: []option.SpeedLimiterGroup{
			{Name: "premium", UploadMbps: 100, DownloadMbps: 200},
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	ul := m.GetOrCreateLimiter("nobody")
	if ul != nil {
		t.Error("expected nil limiter for unknown user with no default")
	}
}

func TestManager_UnknownUser_WithDefault(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Default: &option.SpeedLimiterDefault{
			UploadMbps:   10,
			DownloadMbps: 20,
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	// Unknown user not in users list, but default exists
	ul := m.GetOrCreateLimiter("stranger")
	if ul == nil {
		t.Fatal("expected default limiter for stranger")
	}
	assertRate(t, "stranger upload", ul.Upload, 10)
	assertRate(t, "stranger download", ul.Download, 20)
}

func TestManager_SameInstance(t *testing.T) {
	m, err := NewLimiterManager(baseOptions())
	if err != nil {
		t.Fatal(err)
	}

	ul1 := m.GetOrCreateLimiter("alice")
	ul2 := m.GetOrCreateLimiter("alice")
	if ul1 != ul2 {
		t.Error("expected same limiter instance for same user")
	}
}

func TestManager_PartialOverride(t *testing.T) {
	m, err := NewLimiterManager(baseOptions())
	if err != nil {
		t.Fatal(err)
	}

	// dave: group=premium(100/200), upload override=30
	ul := m.GetOrCreateLimiter("dave")
	if ul == nil {
		t.Fatal("expected limiter for dave")
	}
	// upload: per-user override = 30
	assertRate(t, "dave upload", ul.Upload, 30)
	// download: group premium = 200
	assertRate(t, "dave download", ul.Download, 200)
}

func TestManager_ZeroMbps_NilLimiter(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Users: []option.SpeedLimiterUser{
			{Name: "nolimit", UploadMbps: 0, DownloadMbps: 0},
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	ul := m.GetOrCreateLimiter("nolimit")
	if ul != nil {
		t.Error("expected nil limiter for user with 0 Mbps both directions")
	}
}

func TestManager_ConcurrentGetOrCreate(t *testing.T) {
	m, err := NewLimiterManager(baseOptions())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make([]*UserLimiter, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = m.GetOrCreateLimiter("alice")
		}(i)
	}
	wg.Wait()

	// All should be the same instance
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Fatalf("goroutine %d got different limiter instance", i)
		}
	}
}

func TestManager_Schedule_Apply(t *testing.T) {
	opts := baseOptions()
	opts.Schedules = []option.SpeedLimiterSchedule{
		{TimeRange: "18:00-23:00", UploadMbps: 50, DownloadMbps: 80},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	// Create limiter for alice (premium: 100/200)
	ul := m.GetOrCreateLimiter("alice")
	assertRate(t, "alice upload before schedule", ul.Upload, 100)

	// Simulate entering schedule at 19:00
	m.CheckSchedules(timeAt(19, 0))
	assertRate(t, "alice upload during schedule", ul.Upload, 50)
	assertRate(t, "alice download during schedule", ul.Download, 80)
}

func TestManager_Schedule_Restore(t *testing.T) {
	opts := baseOptions()
	opts.Schedules = []option.SpeedLimiterSchedule{
		{TimeRange: "18:00-23:00", UploadMbps: 50, DownloadMbps: 80},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	ul := m.GetOrCreateLimiter("alice")

	// Enter schedule
	m.CheckSchedules(timeAt(19, 0))
	assertRate(t, "alice upload during", ul.Upload, 50)

	// Exit schedule
	m.CheckSchedules(timeAt(23, 30))
	assertRate(t, "alice upload restored", ul.Upload, 100)
	assertRate(t, "alice download restored", ul.Download, 200)
}

func TestManager_Schedule_CrossMidnight(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Default: &option.SpeedLimiterDefault{UploadMbps: 50, DownloadMbps: 100},
		Schedules: []option.SpeedLimiterSchedule{
			{TimeRange: "23:00-06:00", UploadMbps: 200, DownloadMbps: 500},
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	ul := m.GetOrCreateLimiter("anyuser")

	// Before schedule
	m.CheckSchedules(timeAt(22, 0))
	assertRate(t, "before midnight upload", ul.Upload, 50)

	// During schedule (after midnight)
	m.CheckSchedules(timeAt(2, 0))
	assertRate(t, "after midnight upload", ul.Upload, 200)

	// During schedule (before midnight)
	m.activeSchedul = -1 // reset
	m.CheckSchedules(timeAt(23, 30))
	assertRate(t, "before midnight in schedule upload", ul.Upload, 200)

	// After schedule
	m.CheckSchedules(timeAt(7, 0))
	assertRate(t, "after schedule upload", ul.Upload, 50)
}

func TestManager_Schedule_OverlapPriority(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Default: &option.SpeedLimiterDefault{UploadMbps: 50, DownloadMbps: 100},
		Schedules: []option.SpeedLimiterSchedule{
			{TimeRange: "08:00-22:00", UploadMbps: 30, DownloadMbps: 60},
			{TimeRange: "18:00-22:00", UploadMbps: 10, DownloadMbps: 20}, // higher priority (later)
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	ul := m.GetOrCreateLimiter("user1")

	// In overlapping period: later schedule wins
	m.CheckSchedules(timeAt(19, 0))
	assertRate(t, "overlap upload", ul.Upload, 10)
	assertRate(t, "overlap download", ul.Download, 20)
}

func TestManager_Schedule_GroupFilter(t *testing.T) {
	opts := baseOptions()
	opts.Schedules = []option.SpeedLimiterSchedule{
		{TimeRange: "18:00-23:00", UploadMbps: 50, DownloadMbps: 80, Groups: []string{"premium"}},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	ulAlice := m.GetOrCreateLimiter("alice") // premium
	ulBob := m.GetOrCreateLimiter("bob")     // basic

	m.CheckSchedules(timeAt(19, 0))

	// Alice (premium) should be affected
	assertRate(t, "alice upload schedule", ulAlice.Upload, 50)

	// Bob (basic) should NOT be affected
	assertRate(t, "bob upload no schedule", ulBob.Upload, 10) // original group rate
}

func TestManager_Schedule_EmptyGroups_AllUsers(t *testing.T) {
	opts := baseOptions()
	opts.Schedules = []option.SpeedLimiterSchedule{
		{TimeRange: "18:00-23:00", UploadMbps: 5, DownloadMbps: 10},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	ulAlice := m.GetOrCreateLimiter("alice")
	ulBob := m.GetOrCreateLimiter("bob")

	m.CheckSchedules(timeAt(19, 0))

	// Both affected (no group filter)
	assertRate(t, "alice upload", ulAlice.Upload, 5)
	assertRate(t, "bob upload", ulBob.Upload, 5)
}

func TestManager_Schedule_PerUserOverridePriority(t *testing.T) {
	opts := baseOptions()
	opts.Schedules = []option.SpeedLimiterSchedule{
		{TimeRange: "18:00-23:00", UploadMbps: 50, DownloadMbps: 80},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	// charlie has per-user override: upload=5, download=10
	ulCharlie := m.GetOrCreateLimiter("charlie")

	m.CheckSchedules(timeAt(19, 0))

	// Per-user override takes priority: schedule should NOT change charlie's rates
	assertRate(t, "charlie upload stays", ulCharlie.Upload, 5)
	assertRate(t, "charlie download stays", ulCharlie.Download, 10)
}

func TestManager_Schedule_PartialOverrideWithSchedule(t *testing.T) {
	opts := baseOptions()
	opts.Schedules = []option.SpeedLimiterSchedule{
		{TimeRange: "18:00-23:00", UploadMbps: 50, DownloadMbps: 80},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	// dave: group=premium(100/200), upload override=30
	ulDave := m.GetOrCreateLimiter("dave")

	m.CheckSchedules(timeAt(19, 0))

	// Upload: per-user override = 30, schedule should NOT change it
	assertRate(t, "dave upload stays", ulDave.Upload, 30)
	// Download: no per-user override, schedule SHOULD change it
	assertRate(t, "dave download schedule", ulDave.Download, 80)
}

func TestManager_InvalidGroup(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Users: []option.SpeedLimiterUser{
			{Name: "bad", Group: "nonexistent"},
		},
	}
	_, err := NewLimiterManager(opts)
	if err == nil {
		t.Error("expected error for unknown group reference")
	}
}

func TestManager_InvalidTimeRange(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Schedules: []option.SpeedLimiterSchedule{
			{TimeRange: "invalid"},
		},
	}
	_, err := NewLimiterManager(opts)
	if err == nil {
		t.Error("expected error for invalid time range")
	}
}

func TestManager_ScheduleLoop(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Default: &option.SpeedLimiterDefault{UploadMbps: 10, DownloadMbps: 20},
		Schedules: []option.SpeedLimiterSchedule{
			{TimeRange: "00:00-23:59", UploadMbps: 5, DownloadMbps: 10},
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}

	// Create limiter before starting schedule loop
	ul := m.GetOrCreateLimiter("user1")
	assertRate(t, "before schedule upload", ul.Upload, 10)

	ctx, cancel := context.WithCancel(context.Background())
	m.StartScheduleLoop(ctx)

	// Give it a moment to do initial check
	time.Sleep(200 * time.Millisecond)

	// Schedule should be active (covers almost all day) and have updated the limiter
	assertRate(t, "schedule active upload", ul.Upload, 5)

	cancel()
}

// parseTime edge cases
func TestParseSchedule_Valid(t *testing.T) {
	s := option.SpeedLimiterSchedule{
		TimeRange:    "08:30-17:45",
		UploadMbps:   10,
		DownloadMbps: 20,
	}
	entry, err := parseSchedule(s)
	if err != nil {
		t.Fatal(err)
	}
	if entry.startHour != 8 || entry.startMin != 30 {
		t.Errorf("start: expected 08:30, got %02d:%02d", entry.startHour, entry.startMin)
	}
	if entry.endHour != 17 || entry.endMin != 45 {
		t.Errorf("end: expected 17:45, got %02d:%02d", entry.endHour, entry.endMin)
	}
}

func TestMatchesTime(t *testing.T) {
	tests := []struct {
		name     string
		start    string
		end      string
		hour     int
		min      int
		expected bool
	}{
		{"normal in range", "08:00", "18:00", 12, 0, true},
		{"normal before", "08:00", "18:00", 7, 0, false},
		{"normal at start", "08:00", "18:00", 8, 0, true},
		{"normal at end", "08:00", "18:00", 18, 0, false},
		{"cross midnight in range after", "23:00", "06:00", 23, 30, true},
		{"cross midnight in range before", "23:00", "06:00", 2, 0, true},
		{"cross midnight out of range", "23:00", "06:00", 12, 0, false},
		{"cross midnight at start", "23:00", "06:00", 23, 0, true},
		{"cross midnight at end", "23:00", "06:00", 6, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := option.SpeedLimiterSchedule{TimeRange: tt.start + "-" + tt.end}
			entry, err := parseSchedule(s)
			if err != nil {
				t.Fatal(err)
			}
			result := entry.matchesTime(tt.hour, tt.min)
			if result != tt.expected {
				t.Errorf("matchesTime(%d:%02d) for %s-%s: got %v, want %v",
					tt.hour, tt.min, tt.start, tt.end, result, tt.expected)
			}
		})
	}
}

// helpers

func timeAt(hour, min int) time.Time {
	return time.Date(2026, 4, 7, hour, min, 0, 0, time.Local)
}

func assertRate(t *testing.T, label string, limiter *rate.Limiter, expectedMbps int) {
	t.Helper()
	if limiter == nil {
		if expectedMbps > 0 {
			t.Errorf("%s: limiter is nil, expected %d Mbps", label, expectedMbps)
		}
		return
	}
	expectedRate := rate.Limit(float64(expectedMbps) * float64(MbpsToBps))
	actual := limiter.Limit()
	if actual != expectedRate {
		t.Errorf("%s: rate = %v, expected %v (%d Mbps)", label, actual, expectedRate, expectedMbps)
	}
}
