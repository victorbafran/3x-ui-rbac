package entity

// AdminView is an admin row without its password hash. Permissions holds only
// the admin's own overrides; InboundIds only the inbounds handed to them.
type AdminView struct {
	Id           int             `json:"id" example:"2"`
	Username     string          `json:"username" example:"agent"`
	Role         string          `json:"role" example:"reseller"`
	Enabled      bool            `json:"enabled" example:"true"`
	TrafficQuota int64           `json:"trafficQuota" example:"0"`
	UsersLimit   int64           `json:"usersLimit" example:"0"`
	ExpireAt     int64           `json:"expireAt" example:"0"`
	Permissions  map[string]bool `json:"permissions"`
	InboundIds   []int           `json:"inboundIds"`
}

// AdminMe is what the signed-in admin learns about themselves: enough for the
// sidebar to hide what they may not open, and for the overview to show their
// own usage against their limits. Permissions here are effective.
type AdminMe struct {
	Id           int             `json:"id" example:"2"`
	Username     string          `json:"username" example:"agent"`
	Role         string          `json:"role" example:"reseller"`
	TrafficQuota int64           `json:"trafficQuota" example:"0"`
	UsersLimit   int64           `json:"usersLimit" example:"0"`
	ExpireAt     int64           `json:"expireAt" example:"0"`
	Permissions  map[string]bool `json:"permissions"`
}

// PermissionCatalog lists every switch and each role's template, so the
// admins page renders from the server's closed set rather than its own copy.
type PermissionCatalog struct {
	Keys      []string                   `json:"keys"`
	Templates map[string]map[string]bool `json:"templates"`
}
