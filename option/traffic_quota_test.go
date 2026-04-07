package option

import (
	"bytes"
	"testing"
	"time"

	"github.com/sagernet/sing/common/json"
)

func TestTrafficQuotaServiceOptionsJSON(t *testing.T) {
	rawOptions := []byte(`{
		"groups": [
			{
				"name": "basic",
				"quota_gb": 100,
				"period": "monthly"
			}
		],
		"users": [
			{
				"name": "alice",
				"group": "basic"
			},
			{
				"name": "bob",
				"quota_gb": 0.5,
				"period": "custom",
				"period_start": "2026-04-01T00:00:00Z",
				"period_days": 7
			}
		],
		"persistence": {
			"redis": {
				"address": "127.0.0.1:6379",
				"db": 1,
				"key_prefix": "tq:"
			},
			"postgres": {
				"connection_string": "postgres://localhost/test",
				"table": "traffic_quota"
			}
		},
		"flush_interval": "30s"
	}`)

	var options TrafficQuotaServiceOptions
	if err := json.Unmarshal(rawOptions, &options); err != nil {
		t.Fatalf("unmarshal options: %v", err)
	}

	if len(options.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(options.Groups))
	}
	if options.Groups[0].Name != "basic" {
		t.Fatalf("unexpected group name: %s", options.Groups[0].Name)
	}
	if options.Groups[0].QuotaGB != 100 {
		t.Fatalf("unexpected group quota: %v", options.Groups[0].QuotaGB)
	}
	if options.Groups[0].Period != "monthly" {
		t.Fatalf("unexpected group period: %s", options.Groups[0].Period)
	}

	if len(options.Users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(options.Users))
	}
	if options.Users[0].Group != "basic" {
		t.Fatalf("unexpected user group: %s", options.Users[0].Group)
	}
	if options.Users[1].QuotaGB != 0.5 {
		t.Fatalf("unexpected user quota: %v", options.Users[1].QuotaGB)
	}
	if options.Users[1].PeriodStart != "2026-04-01T00:00:00Z" {
		t.Fatalf("unexpected period start: %s", options.Users[1].PeriodStart)
	}
	if options.Users[1].PeriodDays != 7 {
		t.Fatalf("unexpected period days: %d", options.Users[1].PeriodDays)
	}

	if options.Persistence == nil || options.Persistence.Redis == nil {
		t.Fatal("expected redis persistence options")
	}
	if options.Persistence.Redis.KeyPrefix != "tq:" {
		t.Fatalf("unexpected redis key prefix: %s", options.Persistence.Redis.KeyPrefix)
	}
	if options.Persistence.Postgres == nil {
		t.Fatal("expected postgres persistence options")
	}
	if options.Persistence.Postgres.Table != "traffic_quota" {
		t.Fatalf("unexpected postgres table: %s", options.Persistence.Postgres.Table)
	}

	if time.Duration(options.FlushInterval) != 30*time.Second {
		t.Fatalf("unexpected flush interval: %v", time.Duration(options.FlushInterval))
	}

	marshaled, err := json.Marshal(options)
	if err != nil {
		t.Fatalf("marshal options: %v", err)
	}
	if !bytes.Contains(marshaled, []byte(`"flush_interval":"30s"`)) {
		t.Fatalf("expected marshaled flush interval, got: %s", marshaled)
	}
	if !bytes.Contains(marshaled, []byte(`"key_prefix":"tq:"`)) {
		t.Fatalf("expected marshaled redis key prefix, got: %s", marshaled)
	}
}
