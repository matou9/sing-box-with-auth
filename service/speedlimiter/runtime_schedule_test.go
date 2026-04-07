package speedlimiter

import (
	"testing"

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

func TestManagerRemoveUserSchedules(t *testing.T) {
	opts := option.SpeedLimiterServiceOptions{
		Default: &option.SpeedLimiterDefault{
			UploadMbps:   50,
			DownloadMbps: 100,
		},
		Users: []option.SpeedLimiterUser{
			{Name: "alice"},
		},
	}
	m, err := NewLimiterManager(opts)
	if err != nil {
		t.Fatal(err)
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

	if err := m.RemoveUserSchedules("alice"); err != nil {
		t.Fatal(err)
	}

	if _, ok := m.GetUserSchedules("alice"); ok {
		t.Fatal("expected runtime schedules to be removed")
	}
}
