package database

import (
	"database/sql"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

const (
	superAdminRoleSeeder         = "SuperAdminRoleBackfill"
	resellerInboundClientsSeeder = "ResellerInboundClientsOwner"
)

// seedSuperAdminRole promotes the admins that predate RBAC, who were full
// admins before the column existed. It runs once; later rows carry a real role.
func seedSuperAdminRole() error {
	var count int64
	if err := db.Model(&model.HistoryOfSeeders{}).Where("seeder_name = ?", superAdminRoleSeeder).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).Where("1 = 1").Updates(map[string]any{
			"role":    string(model.RoleSuperAdmin),
			"enabled": true,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&model.HistoryOfSeeders{SeederName: superAdminRoleSeeder}).Error
	})
}

// stampResellerInboundClients gives resellers back the clients they added inside an
// inbound before rbac43 stamped those. Once only: later unowned ones are unscoped admins'.
func stampResellerInboundClients() error {
	var count int64
	if err := db.Model(&model.HistoryOfSeeders{}).Where("seeder_name = ?", resellerInboundClientsSeeder).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		inboundsOf, err := soleResellerInbounds(tx)
		if err != nil {
			return err
		}
		// Read before any stamp: the rows stamped below are older and would move it.
		stampedSince, err := firstStampedClientAt(tx)
		if err != nil {
			return err
		}
		for resellerId, inboundIds := range inboundsOf {
			unowned := tx.Model(&model.ClientRecord{}).
				Where("admin_id = ?", 0).
				Where("EXISTS (SELECT 1 FROM client_inbounds ci WHERE ci.client_id = clients.id)").
				Where("NOT EXISTS (SELECT 1 FROM client_inbounds ci WHERE ci.client_id = clients.id AND ci.inbound_id NOT IN ?)", inboundIds)
			if stampedSince.Valid {
				unowned = unowned.Where("created_at < ?", stampedSince.Int64)
			}
			if err := unowned.UpdateColumn("admin_id", resellerId).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.HistoryOfSeeders{SeederName: resellerInboundClientsSeeder}).Error
	})
}

// firstStampedClientAt is when rbac43 began stamping, as far as the rows show: every
// reseller's client since carries an owner, so an unowned one since is an unscoped admin's.
func firstStampedClientAt(tx *gorm.DB) (sql.NullInt64, error) {
	var first sql.NullInt64
	err := tx.Model(&model.ClientRecord{}).Where("admin_id > ?", 0).Select("MIN(created_at)").Row().Scan(&first)
	return first, err
}

// soleResellerInbounds is, per reseller, the inbounds they created that no other admin
// was assigned: an unowned client in a shared one could be any of its admins'.
func soleResellerInbounds(tx *gorm.DB) (map[int][]int, error) {
	var rows []struct{ Id, UserId int }
	if err := tx.Table("inbounds AS i").
		Select("i.id, i.user_id").
		Joins("JOIN users u ON u.id = i.user_id").
		Where("u.role = ?", string(model.RoleReseller)).
		Where("NOT EXISTS (SELECT 1 FROM admin_inbounds ai WHERE ai.inbound_id = i.id AND ai.admin_id <> i.user_id)").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	inboundsOf := make(map[int][]int)
	for _, r := range rows {
		inboundsOf[r.UserId] = append(inboundsOf[r.UserId], r.Id)
	}
	return inboundsOf, nil
}
