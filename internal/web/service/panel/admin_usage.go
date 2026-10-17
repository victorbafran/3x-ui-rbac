package panel

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

// AdminUsage is one admin's customer count and the traffic those customers
// have used. Traffic follows the clients they own, not the inbounds they work
// on: two resellers can share an inbound, and each must answer for their own.
type AdminUsage struct {
	Up      int64 `json:"up" example:"0"`
	Down    int64 `json:"down" example:"0"`
	Clients int64 `json:"clients" example:"0"`
}

// Total is what a traffic quota is measured against.
func (u AdminUsage) Total() int64 { return u.Up + u.Down }

// UsageByAdmin sums every admin's client traffic in one pass, so a list of
// admins costs one query rather than one per row. Admins with no clients are
// absent from the map; read them as zero.
func UsageByAdmin() (map[int]AdminUsage, error) {
	type row struct {
		AdminId int
		Up      int64
		Down    int64
		Clients int64
	}
	var rows []row
	err := database.GetDB().Table("clients AS c").
		Select("c.admin_id AS admin_id, "+
			"COALESCE(SUM(ct.up), 0) AS up, "+
			"COALESCE(SUM(ct.down), 0) AS down, "+
			"COUNT(c.id) AS clients").
		Joins("LEFT JOIN client_traffics ct ON ct.email = c.email").
		Where("c.admin_id > ?", 0).
		Group("c.admin_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[int]AdminUsage, len(rows))
	for _, r := range rows {
		out[r.AdminId] = AdminUsage{Up: r.Up, Down: r.Down, Clients: r.Clients}
	}
	return out, nil
}

// UsageFor is UsageByAdmin for a single admin.
func UsageFor(adminId int) (AdminUsage, error) {
	var u AdminUsage
	if adminId <= 0 {
		return u, nil
	}
	err := database.GetDB().Table("clients AS c").
		Select("COALESCE(SUM(ct.up), 0) AS up, "+
			"COALESCE(SUM(ct.down), 0) AS down, "+
			"COUNT(c.id) AS clients").
		Joins("LEFT JOIN client_traffics ct ON ct.email = c.email").
		Where("c.admin_id = ?", adminId).
		Scan(&u).Error
	return u, err
}
