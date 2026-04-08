package option

import "github.com/sagernet/sing/common/json/badoption"

// DynamicConfigOptions configures optional runtime-update sources for both
// traffic-quota and speed-limiter services. All fields are optional; if absent
// the service runs in static-JSON-only mode.
type DynamicConfigOptions struct {
	Postgres *DynamicPostgresOptions `json:"postgres,omitempty"`
	Redis    *DynamicRedisOptions    `json:"redis,omitempty"`
}

// DynamicPostgresOptions configures the Postgres LISTEN/NOTIFY source.
type DynamicPostgresOptions struct {
	ConnectionString string             `json:"connection_string"`
	// Table is the users table (default: "users").
	Table            string             `json:"table,omitempty"`
	// NotifyChannel is the LISTEN channel name (default: "user_changes").
	NotifyChannel    string             `json:"notify_channel,omitempty"`
	// UpdateInterval is the polling fallback interval (default: 1 minute).
	UpdateInterval   badoption.Duration `json:"update_interval,omitempty"`
}

// DynamicRedisOptions configures the Redis Pub/Sub source.
type DynamicRedisOptions struct {
	Address  string `json:"address"`
	Password string `json:"password,omitempty"`
	DB       int    `json:"db,omitempty"`
	// Channel is the Pub/Sub channel for live updates (default: "sing-box:config:updates").
	Channel string `json:"channel,omitempty"`
	// HashKey is a Redis hash (field=username, value=JSON ConfigRow) polled on interval.
	HashKey        string             `json:"hash_key,omitempty"`
	// UpdateInterval is the hash polling interval (default: 30 seconds).
	UpdateInterval badoption.Duration `json:"update_interval,omitempty"`
}
