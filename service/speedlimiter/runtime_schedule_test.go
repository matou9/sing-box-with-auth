package speedlimiter

import (
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
)

func TestManagerUserScheduleAppliesWhenNoFixedOverride(t *testing.T) {
	m, err := NewLimiterManager(baseOptions())
	if err != nil {
		t.Fatal(err)
	}

	ul := m.GetOrCreateLimiter("alice")
	if ul == nil {
		t.Fatal("expected limiter for alice")
	}
	assertRate(t, "alice upload before runtime schedule", ul.Upload, 100)
	assertRate(t, "alice download before runtime schedule", ul.Download, 200)

	schedules := []UserSchedule{
		{
			TimeRange:    "18:00-23:00",
			UploadMbps:   25,
			DownloadMbps: 40,
		},
	}
	if err := m.ReplaceUserSchedules("alice", schedules); err != nil {
		t.Fatal(err)
	}

	got, ok := m.GetUserSchedules("alice")
	if !ok {
		t.Fatal("expected runtime schedules for alice")
	}
	if len(got) != len(schedules) {
		t.Fatalf("schedule length = %d, want %d", len(got), len(schedules))
	}
	if got[0] != schedules[0] {
		t.Fatalf("schedule = %+v, want %+v", got[0], schedules[0])
	}

	m.CheckSchedules(timeAt(19, 0))
	assertRate(t, "alice upload during runtime schedule", ul.Upload, 25)
	assertRate(t, "alice download during runtime schedule", ul.Download, 40)

	uploadMbps, downloadMbps, ok := m.CurrentSpeed("alice")
	if !ok {
		t.Fatal("expected current speed for alice")
	}
	if uploadMbps != 25 || downloadMbps != 40 {
		t.Fatalf("current speed = %d/%d, want 25/40", uploadMbps, downloadMbps)
	}
}

func TestManagerFixedUserSpeedBeatsUserSchedule(t *testing.T) {
	m, err := NewLimiterManager(baseOptions())
	if err != nil {
		t.Fatal(err)
	}

	ul := m.GetOrCreateLimiter("charlie")
	if ul == nil {
		t.Fatal("expected limiter for charlie")
	}
	assertRate(t, "charlie upload before runtime schedule", ul.Upload, 5)
	assertRate(t, "charlie download before runtime schedule", ul.Download, 10)

	if err := m.ReplaceUserSchedules("charlie", []UserSchedule{
		{
			TimeRange:    "18:00-23:00",
			UploadMbps:   25,
			DownloadMbps: 40,
		},
	}); err != nil {
		t.Fatal(err)
	}

	m.CheckSchedules(timeAt(19, 0))
	assertRate(t, "charlie upload stays fixed", ul.Upload, 5)
	assertRate(t, "charlie download stays fixed", ul.Download, 10)

	uploadMbps, downloadMbps, ok := m.CurrentSpeed("charlie")
	if !ok {
		t.Fatal("expected current speed for charlie")
	}
	if uploadMbps != 5 || downloadMbps != 10 {
		t.Fatalf("current speed = %d/%d, want 5/10", uploadMbps, downloadMbps)
	}
}

func TestManagerUserScheduleBeatsGlobalSchedule(t *testing.T) {
	opts := baseOptions()
	opts.Schedules = []option.SpeedLimiterSchedule{
		{TimeRange: "18:00-23:00", UploadMbps: 50, DownloadMbps: 80},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return timeAt(19, 0) }

	ul := m.GetOrCreateLimiter("alice")
	if ul == nil {
		t.Fatal("expected limiter for alice")
	}

	if err := m.ReplaceUserSchedules("alice", []UserSchedule{
		{
			TimeRange:    "18:00-23:00",
			UploadMbps:   25,
			DownloadMbps: 40,
		},
	}); err != nil {
		t.Fatal(err)
	}

	m.CheckSchedules(timeAt(19, 0))
	assertRate(t, "alice upload prefers user schedule", ul.Upload, 25)
	assertRate(t, "alice download prefers user schedule", ul.Download, 40)

	uploadMbps, downloadMbps, ok := m.CurrentSpeed("alice")
	if !ok {
		t.Fatal("expected current speed for alice")
	}
	if uploadMbps != 25 || downloadMbps != 40 {
		t.Fatalf("current speed = %d/%d, want 25/40", uploadMbps, downloadMbps)
	}
}

func TestManagerRemoveUserSchedulesFallsBackToGlobalSchedule(t *testing.T) {
	opts := baseOptions()
	opts.Schedules = []option.SpeedLimiterSchedule{
		{TimeRange: "18:00-23:00", UploadMbps: 50, DownloadMbps: 80},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return timeAt(19, 0) }

	ul := m.GetOrCreateLimiter("alice")
	if ul == nil {
		t.Fatal("expected limiter for alice")
	}

	if err := m.ReplaceUserSchedules("alice", []UserSchedule{
		{
			TimeRange:    "18:00-23:00",
			UploadMbps:   25,
			DownloadMbps: 40,
		},
	}); err != nil {
		t.Fatal(err)
	}

	m.CheckSchedules(timeAt(19, 0))
	assertRate(t, "alice upload during user schedule", ul.Upload, 25)
	assertRate(t, "alice download during user schedule", ul.Download, 40)

	if err := m.RemoveUserSchedules("alice"); err != nil {
		t.Fatal(err)
	}

	if _, ok := m.GetUserSchedules("alice"); ok {
		t.Fatal("expected runtime schedules to be removed")
	}

	assertRate(t, "alice upload falls back to global schedule", ul.Upload, 50)
	assertRate(t, "alice download falls back to global schedule", ul.Download, 80)

	uploadMbps, downloadMbps, ok := m.CurrentSpeed("alice")
	if !ok {
		t.Fatal("expected current speed for alice")
	}
	if uploadMbps != 50 || downloadMbps != 80 {
		t.Fatalf("current speed after removal = %d/%d, want 50/80", uploadMbps, downloadMbps)
	}
}

func TestManagerRemoveUserSchedulesEvictsRuntimeOnlyLimiter(t *testing.T) {
	m, err := NewLimiterManager(option.SpeedLimiterServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return timeAt(19, 0) }

	if err := m.ReplaceUserSchedules("runtime-only", []UserSchedule{
		{
			TimeRange:    "18:00-23:00",
			UploadMbps:   25,
			DownloadMbps: 40,
		},
	}); err != nil {
		t.Fatal(err)
	}

	ul := m.GetOrCreateLimiter("runtime-only")
	if ul == nil {
		t.Fatal("expected limiter for runtime-only user while schedule is active")
	}
	assertRate(t, "runtime-only upload during schedule", ul.Upload, 25)
	assertRate(t, "runtime-only download during schedule", ul.Download, 40)

	if err := m.RemoveUserSchedules("runtime-only"); err != nil {
		t.Fatal(err)
	}

	if _, _, ok := m.CurrentSpeed("runtime-only"); ok {
		t.Fatal("expected no current speed for runtime-only user after schedule removal")
	}
	if got := m.GetOrCreateLimiter("runtime-only"); got != nil {
		t.Fatal("expected limiter eviction for runtime-only user after schedule removal")
	}
}

func TestManagerCachedLimiterCreatesUploadDirectionWhenScheduleActivates(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Users: []option.SpeedLimiterUser{
			{Name: "download-only", DownloadMbps: 10},
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return timeAt(19, 0) }

	ul := m.GetOrCreateLimiter("download-only")
	if ul == nil {
		t.Fatal("expected limiter for download-only user")
	}
	if ul.Upload != nil {
		t.Fatal("expected upload limiter to start nil")
	}
	assertRate(t, "download-only base download", ul.Download, 10)

	if err := m.ReplaceUserSchedules("download-only", []UserSchedule{
		{
			TimeRange:    "18:00-23:00",
			UploadMbps:   25,
			DownloadMbps: 40,
		},
	}); err != nil {
		t.Fatal(err)
	}

	if ul.Upload == nil {
		t.Fatal("expected upload limiter to be created when runtime schedule enables upload")
	}
	assertRate(t, "download-only upload during schedule", ul.Upload, 25)
	assertRate(t, "download-only download stays fixed", ul.Download, 10)
}

func TestManagerCachedLimiterRemovesUploadDirectionWhenFallbackHitsZero(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Users: []option.SpeedLimiterUser{
			{Name: "download-only", DownloadMbps: 10},
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return timeAt(19, 0) }

	ul := m.GetOrCreateLimiter("download-only")
	if ul == nil {
		t.Fatal("expected limiter for download-only user")
	}

	if err := m.ReplaceUserSchedules("download-only", []UserSchedule{
		{
			TimeRange:    "18:00-23:00",
			UploadMbps:   25,
			DownloadMbps: 40,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if ul.Upload == nil {
		t.Fatal("expected upload limiter during runtime schedule")
	}

	if err := m.RemoveUserSchedules("download-only"); err != nil {
		t.Fatal(err)
	}

	if ul.Upload != nil {
		t.Fatal("expected upload limiter to be removed when fallback upload is 0")
	}
	assertRate(t, "download-only download after fallback", ul.Download, 10)

	uploadMbps, downloadMbps, ok := m.CurrentSpeed("download-only")
	if !ok {
		t.Fatal("expected current speed for download-only user")
	}
	if uploadMbps != 0 || downloadMbps != 10 {
		t.Fatalf("current speed after fallback = %d/%d, want 0/10", uploadMbps, downloadMbps)
	}
}
