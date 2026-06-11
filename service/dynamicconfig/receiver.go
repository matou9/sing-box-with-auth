package dynamicconfig

// ConfigRow carries the per-user dynamic config fields read from Postgres or Redis.
// Zero values mean "not set / use static JSON defaults".
type ConfigRow struct {
	User         string
	UploadMbps   int
	DownloadMbps int
	PerClient    *bool
	QuotaGB      float64
	Period       string
	PeriodStart  string
	PeriodDays   int
	// Authoritative marks rows that carry the user's complete config (a
	// Postgres row). In an authoritative row a zero field means "unset" and
	// must clear the corresponding runtime config; in a partial update (a
	// Redis message) a zero field means "leave unchanged".
	Authoritative bool
}

// Receiver is the callback pair that dynamic sources call when a user config
// changes or is removed. Implementations must be safe for concurrent use.
type Receiver struct {
	Apply  func(row ConfigRow) error
	Remove func(user string) error
}
