package database

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

const superAdminRoleSeeder = "SuperAdminRoleBackfill"

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
