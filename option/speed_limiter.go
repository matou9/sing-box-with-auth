package option

type SpeedLimiterServiceOptions struct {
	Default   *SpeedLimiterDefault  `json:"default,omitempty"`
	Groups    []SpeedLimiterGroup   `json:"groups,omitempty"`
	Users     []SpeedLimiterUser    `json:"users,omitempty"`
	Schedules []SpeedLimiterSchedule `json:"schedules,omitempty"`
	Dynamic   *DynamicConfigOptions `json:"dynamic,omitempty"`
}

type SpeedLimiterDefault struct {
	UploadMbps       int `json:"upload_mbps,omitempty"`
	DownloadMbps     int `json:"download_mbps,omitempty"`
	PerClient        bool `json:"per_client,omitempty"`
	ClientTTLMinutes int  `json:"client_ttl_minutes,omitempty"`
}

type SpeedLimiterGroup struct {
	Name         string `json:"name"`
	UploadMbps   int    `json:"upload_mbps,omitempty"`
	DownloadMbps int    `json:"download_mbps,omitempty"`
	PerClient    *bool  `json:"per_client,omitempty"`
}

type SpeedLimiterUser struct {
	Name         string `json:"name"`
	Group        string `json:"group,omitempty"`
	UploadMbps   int    `json:"upload_mbps,omitempty"`
	DownloadMbps int    `json:"download_mbps,omitempty"`
	PerClient    *bool  `json:"per_client,omitempty"`
}

type SpeedLimiterSchedule struct {
	TimeRange    string   `json:"time_range"`
	UploadMbps   int      `json:"upload_mbps,omitempty"`
	DownloadMbps int      `json:"download_mbps,omitempty"`
	Groups       []string `json:"groups,omitempty"`
}
