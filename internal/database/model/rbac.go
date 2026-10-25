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

// RelayRole is which way the traffic goes through a relay, which is the whole
// difference between the two kinds of machine the panel provisions.
//
// An exit changes where a customer appears to be: the panel sends their
// traffic out through it. A front changes where they connect: it accepts them
// in a country they can reach and carries them to the panel. The enrollment,
// the key, the country lookup and the health reporting are the same for both,
// which is why they are one table rather than two.
type RelayRole string

const (
	// RelayExit is the default, so every relay enrolled before this existed
	// keeps meaning exactly what it meant.
	RelayExit  RelayRole = "exit"
	RelayFront RelayRole = "front"
)

func (r RelayRole) Valid() bool {
	return r == RelayExit || r == RelayFront
}

// Relay is a remote tunnel host the panel provisions over SSH. The SSH password
// is used once at enrollment and never stored; only KeyPath outlives it.
type Relay struct {
	Id      int    `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	Name    string `json:"name" gorm:"uniqueIndex;not null" example:"relay-tehran-1"`
	Address string `json:"address" gorm:"not null" example:"185.0.0.1"`
	SSHPort int    `json:"sshPort" gorm:"not null;default:22" example:"22"`
	SSHUser string `json:"sshUser" gorm:"not null;default:root" example:"root"`
	KeyPath string `json:"keyPath" gorm:"not null" example:"/etc/x-ui/relay-keys/relay-tehran-1"`
	Tunnel  string `json:"tunnel" gorm:"not null;default:unknown" example:"backhaul"`

	// Role is exit or front. Defaulted in the column so a database written
	// before fronts existed reads back as a table full of exits, which is
	// what those rows are.
	Role RelayRole `json:"role" gorm:"not null;default:exit" example:"front"`

	// The rest of this block is a front's and is left empty on an exit.
	//
	// TunnelPort is where the front listens for the panel to dial in. It is
	// not a customer port: nothing but the panel should ever reach it.
	TunnelPort int `json:"tunnelPort" gorm:"column:tunnel_port" example:"3080"`
	// Transport is how the tunnel carries its traffic. Offered because a
	// network that blocks one may pass another — not as a speed control:
	// measured on a clean path, the tunnel ran at 98% of the raw link
	// whichever one was chosen.
	Transport string `json:"transport" example:"tcp"`
	// PublicName is the name customers type to connect through this front,
	// pointed at its address. Optional, but a TLS handshake from inside Iran
	// does not complete without a name, so a cisco customer behind a front
	// needs one; the address alone is not enough.
	PublicName string `json:"publicName" gorm:"column:public_name;not null;default:''" example:"tun.example.com"`
	// Address6 is the front's public IPv6, bare; read off the server unless typed.
	Address6 string `json:"address6" gorm:"column:address6;not null;default:''" example:"2001:db8::1"`
	// IPFamily is what customers are given: "" IPv4 as before, "v6", or "both".
	IPFamily string `json:"ipFamily" gorm:"column:ip_family;not null;default:''" example:"both"`
	// Address6Error is why the panel could not read the server's IPv6 (Address6
	// still "") or reach Address6; "" once it could.
	Address6Error string `json:"address6Error" gorm:"column:address6_error;not null;default:''"`
	// TunnelToken authenticates the two ends to each other. Encrypted at rest
	// the same way the exit's tunnel key is, and never returned to the panel
	// UI: a tunnel whose token leaks is one anybody who finds the port can
	// join.
	TunnelToken string `json:"-" gorm:"column:tunnel_token"`
	NodeId      int    `json:"nodeId" gorm:"index" example:"2"`
	Status      string `json:"status" gorm:"not null;default:pending" example:"pending"`
	LastError   string `json:"lastError"`
	LastSeen    int64  `json:"lastSeen" example:"0"`
	Enabled     bool   `json:"enabled" gorm:"not null;default:true" example:"true"`
	CreatedAt   int64  `json:"createdAt" gorm:"autoCreateTime" example:"1760000000"`

	// Label is what a customer reads when this relay is offered as a country,
	// where Name is the identifier the VPN server matches on.
	Label string `json:"label" example:"Netherlands"`

	// WgPrivate is the panel's half of the tunnel key and WgPeerPublic the
	// relay's. Settled once at enrollment and reused, so bringing the tunnel
	// back up after a reboot does not rekey it and drop every customer on
	// that country. WgPrivate is encrypted at rest wherever the panel has a
	// key to encrypt it with, the same as a node's API token.
	WgPrivate    string `json:"-" gorm:"column:wg_private"`
	WgPeerPublic string `json:"wgPeerPublic" gorm:"column:wg_peer_public"`

	// Where the relay turned out to be, asked of the relay itself once it is
	// reachable. The operator knows the address they bought, not the city it
	// ended up in: one bought as "Holand" answered from France.
	Country     string `json:"country"`
	CountryCode string `json:"countryCode" gorm:"column:country_code"`
	City        string `json:"city"`
}

// AdminInbound hands an inbound to an admin who did not create it. Ownership
// is "created by, or assigned to"; a default reseller only ever has the latter.
type AdminInbound struct {
	AdminId   int   `json:"adminId" gorm:"primaryKey;column:admin_id;index"`
	InboundId int   `json:"inboundId" gorm:"primaryKey;column:inbound_id;index"`
	CreatedAt int64 `json:"createdAt" gorm:"autoCreateTime"`
}

func (AdminInbound) TableName() string { return "admin_inbounds" }
