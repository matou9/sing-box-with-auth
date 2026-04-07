package adapter

// User represents a universal user definition that contains fields
// for all supported proxy protocols. Each protocol extracts the
// fields it needs.
type User struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
	UUID     string `json:"uuid,omitempty"`
	AlterId  int    `json:"alter_id,omitempty"`
	Flow     string `json:"flow,omitempty"`
}

// ManagedUserServer is implemented by inbound protocols that support
// dynamic user management via the user-provider service.
type ManagedUserServer interface {
	Inbound
	ReplaceUsers(users []User) error
}
