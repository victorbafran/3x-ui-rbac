package model

// PanelClone is a copy of this panel installed on another server from its full
// backup and kept on standby, ready to be made the main panel.
type PanelClone struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Address   string `json:"address" gorm:"not null"`
	SSHPort   int    `json:"sshPort" gorm:"not null;default:22"`
	SSHUser   string `json:"sshUser" gorm:"not null;default:root"`
	KeyPath   string `json:"-" gorm:"not null;default:''"`
	Status    string `json:"status" gorm:"not null;default:'pending'"` // pending|installing|standby|main|error
	Step      string `json:"step" gorm:"not null;default:''"`          // current step label while working
	Mode      string `json:"mode" gorm:"not null;default:''"`          // release|copied
	Version   string `json:"version" gorm:"not null;default:''"`
	LastError string `json:"lastError" gorm:"not null;default:''"`
	LastSeen  int64  `json:"lastSeen"`
	LastSync  int64  `json:"lastSync"`
	CreatedAt int64  `json:"createdAt" gorm:"autoCreateTime"`
}
