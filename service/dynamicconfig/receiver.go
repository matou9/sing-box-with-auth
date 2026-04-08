package dynamicconfig

// ConfigRow carries the per-user dynamic config fields read from Postgres or Redis.
// Zero values mean "not set / use static JSON defaults".
type ConfigRow struct {
	User         string
	UploadMbps   int
	DownloadMbps int
	QuotaGB      float64
	Period       string
	PeriodStart  string
	PeriodDays   int
}

// Receiver is the callback pair that dynamic sources call when a user config
// changes or is removed. Implementations must be safe for concurrent use.
type Receiver struct {
	Apply  func(row ConfigRow) error
	Remove func(user string) error
}
