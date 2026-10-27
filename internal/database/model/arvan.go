package model

// ArvanFront binds an entry server to the ArvanCloud machine and DNS record it
// lives on, so the panel can move its public IP to a tested reserve.
type ArvanFront struct {
	Id            int    `json:"id" gorm:"primaryKey;autoIncrement"`
	RelayId       int    `json:"relayId" gorm:"uniqueIndex;not null"`
	Zone          string `json:"zone" gorm:"not null;default:''"`
	ServerId      string `json:"serverId" gorm:"not null;default:''"`
	ServerName    string `json:"serverName" gorm:"not null;default:''"`
	Domain        string `json:"domain" gorm:"not null;default:''"`
	RecordName    string `json:"recordName" gorm:"not null;default:''"`
	RecordId      string `json:"recordId" gorm:"not null;default:''"`
	ReserveTarget int    `json:"reserveTarget" gorm:"not null;default:0"`
	AutoSwap      bool   `json:"autoSwap" gorm:"not null;default:false"`
	DailySwapAt   string `json:"dailySwapAt" gorm:"not null;default:''"` // "" or "HH:MM" in the panel's time zone
	Prepared      bool   `json:"prepared" gorm:"not null;default:false"`
	ActiveFails   int    `json:"activeFails" gorm:"not null;default:0"`
	LastCheck     int64  `json:"lastCheck"`
	LastSwap      int64  `json:"lastSwap"`
	LastAction    string `json:"lastAction" gorm:"not null;default:''"`
	LastError     string `json:"lastError" gorm:"not null;default:''"`
	AddsDay       string `json:"-" gorm:"not null;default:''"`
	AddsCount     int    `json:"-" gorm:"not null;default:0"`
}

// ArvanIP is one reserve public IP on a front's Arvan server; the active IP is
// the relay's own Address and has no row here.
type ArvanIP struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement"`
	RelayId   int    `json:"relayId" gorm:"index;not null"`
	IP        string `json:"ip" gorm:"column:ip;uniqueIndex;not null"`
	PortId    string `json:"portId" gorm:"not null;default:''"`
	Status    string `json:"status" gorm:"not null;default:'testing'"` // testing | healthy | dead
	LastCheck int64  `json:"lastCheck"`
	LastError string `json:"lastError" gorm:"not null;default:''"`
	IranOk    int    `json:"iranOk"`
	IranTotal int    `json:"iranTotal"`
	CreatedAt int64  `json:"createdAt" gorm:"autoCreateTime"`
}
