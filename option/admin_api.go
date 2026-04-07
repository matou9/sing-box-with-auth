package option

import "github.com/sagernet/sing/common/json/badoption"

type AdminCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type AdminAPIServiceOptions struct {
	Listen      string             `json:"listen,omitempty"`
	Path        string             `json:"path,omitempty"`
	TokenSecret string             `json:"token_secret,omitempty"`
	TokenTTL    badoption.Duration `json:"token_ttl,omitempty"`
	Tokens      []string           `json:"tokens,omitempty"`
	Admins      []AdminCredential  `json:"admins,omitempty"`
}
