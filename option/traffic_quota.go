package option

import "github.com/sagernet/sing/common/json/badoption"

type TrafficQuotaServiceOptions struct {
	Groups        []TrafficQuotaGroup      `json:"groups,omitempty"`
	Users         []TrafficQuotaUser       `json:"users,omitempty"`
	Persistence   *TrafficQuotaPersistence `json:"persistence,omitempty"`
	FlushInterval badoption.Duration       `json:"flush_interval,omitempty"`
}

type TrafficQuotaGroup struct {
	Name        string  `json:"name"`
	QuotaGB     float64 `json:"quota_gb,omitempty"`
	Period      string  `json:"period,omitempty"`
	PeriodStart string  `json:"period_start,omitempty"`
	PeriodDays  int     `json:"period_days,omitempty"`
}

type TrafficQuotaUser struct {
	Name        string  `json:"name"`
	Group       string  `json:"group,omitempty"`
	QuotaGB     float64 `json:"quota_gb,omitempty"`
	Period      string  `json:"period,omitempty"`
	PeriodStart string  `json:"period_start,omitempty"`
	PeriodDays  int     `json:"period_days,omitempty"`
}

type TrafficQuotaPersistence struct {
	Redis    *TrafficQuotaRedisOptions    `json:"redis,omitempty"`
	Postgres *TrafficQuotaPostgresOptions `json:"postgres,omitempty"`
}

type TrafficQuotaRedisOptions struct {
	Address   string `json:"address"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
	DB        int    `json:"db,omitempty"`
	KeyPrefix string `json:"key_prefix,omitempty"`
}

type TrafficQuotaPostgresOptions struct {
	ConnectionString string `json:"connection_string"`
	Table            string `json:"table,omitempty"`
}
