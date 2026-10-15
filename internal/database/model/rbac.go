package model

// Role is an admin's permission level, stored on User. A row whose role is
// unknown to this build is treated as no permissions, never as super_admin.
type Role string

const (
	RoleSuperAdmin Role = "super_admin"
	RoleManager    Role = "manager"
	RoleReseller   Role = "reseller"
	RoleReadOnly   Role = "readonly"
)

func (r Role) Valid() bool {
	switch r {
	case RoleSuperAdmin, RoleManager, RoleReseller, RoleReadOnly:
		return true
	default:
		return false
	}
}

// Relay is a remote tunnel host the panel provisions over SSH. The SSH password
// is used once at enrollment and never stored; only KeyPath outlives it.
type Relay struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	Name      string `json:"name" gorm:"uniqueIndex;not null" example:"relay-tehran-1"`
	Address   string `json:"address" gorm:"not null" example:"185.0.0.1"`
	SSHPort   int    `json:"sshPort" gorm:"not null;default:22" example:"22"`
	SSHUser   string `json:"sshUser" gorm:"not null;default:root" example:"root"`
	KeyPath   string `json:"keyPath" gorm:"not null" example:"/etc/x-ui/relay-keys/relay-tehran-1"`
	Tunnel    string `json:"tunnel" gorm:"not null;default:unknown" example:"backhaul"`
	NodeId    int    `json:"nodeId" gorm:"index" example:"2"`
	Status    string `json:"status" gorm:"not null;default:pending" example:"pending"`
	LastError string `json:"lastError"`
	LastSeen  int64  `json:"lastSeen" example:"0"`
	Enabled   bool   `json:"enabled" gorm:"not null;default:true" example:"true"`
	CreatedAt int64  `json:"createdAt" gorm:"autoCreateTime" example:"1760000000"`
}
