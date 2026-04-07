package option

type SpeedLimiterServiceOptions struct {
	Default   *SpeedLimiterDefault    `json:"default,omitempty"`
	Groups    []SpeedLimiterGroup     `json:"groups,omitempty"`
	Users     []SpeedLimiterUser      `json:"users,omitempty"`
	Schedules []SpeedLimiterSchedule  `json:"schedules,omitempty"`
}

type SpeedLimiterDefault struct {
	UploadMbps   int `json:"upload_mbps,omitempty"`
	DownloadMbps int `json:"download_mbps,omitempty"`
}

type SpeedLimiterGroup struct {
	Name         string `json:"name"`
	UploadMbps   int    `json:"upload_mbps,omitempty"`
	DownloadMbps int    `json:"download_mbps,omitempty"`
}

type SpeedLimiterUser struct {
	Name         string `json:"name"`
	Group        string `json:"group,omitempty"`
	UploadMbps   int    `json:"upload_mbps,omitempty"`
	DownloadMbps int    `json:"download_mbps,omitempty"`
}

type SpeedLimiterSchedule struct {
	TimeRange    string   `json:"time_range"`
	UploadMbps   int      `json:"upload_mbps,omitempty"`
	DownloadMbps int      `json:"download_mbps,omitempty"`
	Groups       []string `json:"groups,omitempty"`
}
