package model

// PsiphonCountry is one Psiphon exit country of this panel. Its process listens
// on the two loopback ports, which stay with the country once it is given them.
type PsiphonCountry struct {
	Code      string `json:"code" gorm:"primaryKey"`
	Enabled   bool   `json:"enabled" gorm:"not null;default:false"`
	SocksPort int    `json:"socksPort" gorm:"uniqueIndex;not null"`
	HTTPPort  int    `json:"httpPort" gorm:"column:http_port;uniqueIndex;not null"`
	ExitIP    string `json:"exitIp" gorm:"column:exit_ip;not null;default:''"`
	ExitLoc   string `json:"exitLoc" gorm:"not null;default:''"`
	LastCheck int64  `json:"lastCheck"`
	LastError string `json:"lastError" gorm:"not null;default:''"`
	CreatedAt int64  `json:"createdAt" gorm:"autoCreateTime"`
}
