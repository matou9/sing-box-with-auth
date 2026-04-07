package option

import "github.com/sagernet/sing/common/json/badoption"

type UserProviderServiceOptions struct {
	Inbounds []string                       `json:"inbounds"`
	Users    []User                         `json:"users,omitempty"`
	File     *UserProviderFileOptions       `json:"file,omitempty"`
	HTTP     *UserProviderHTTPOptions       `json:"http,omitempty"`
	Redis    *UserProviderRedisOptions      `json:"redis,omitempty"`
	Postgres *UserProviderPostgresOptions   `json:"postgres,omitempty"`
}

type User struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
	UUID     string `json:"uuid,omitempty"`
	AlterId  int    `json:"alter_id,omitempty"`
	Flow     string `json:"flow,omitempty"`
}

type UserProviderFileOptions struct {
	Path           string             `json:"path"`
	UpdateInterval badoption.Duration `json:"update_interval,omitempty"`
}

type UserProviderHTTPOptions struct {
	URL            string             `json:"url"`
	UpdateInterval badoption.Duration `json:"update_interval,omitempty"`
	DownloadDetour string             `json:"download_detour,omitempty"`
}

type UserProviderRedisOptions struct {
	Address        string             `json:"address"`
	Username       string             `json:"username,omitempty"`
	Password       string             `json:"password,omitempty"`
	DB             int                `json:"db,omitempty"`
	Key            string             `json:"key"`
	Channel        string             `json:"channel,omitempty"`
	UpdateInterval badoption.Duration `json:"update_interval,omitempty"`
}

type UserProviderPostgresOptions struct {
	ConnectionString string             `json:"connection_string"`
	Table            string             `json:"table,omitempty"`
	NotifyChannel    string             `json:"notify_channel,omitempty"`
	UpdateInterval   badoption.Duration `json:"update_interval,omitempty"`
}
