package panel

import (
	"errors"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

var ErrInboundNotFound = errors.New("inbound not found")

// Assignments returns the inbound ids handed to admin id, ascending.
func (s *AdminService) Assignments(adminId int) ([]int, error) {
	ids := []int{}
	err := database.GetDB().Model(&model.AdminInbound{}).
		Where("admin_id = ?", adminId).Order("inbound_id ASC").
		Pluck("inbound_id", &ids).Error
	return ids, err
}

// SetAssignments replaces the admin's assignments with inboundIds. Every id
// must exist, so a stale picker in the UI cannot leave a dangling row.
func (s *AdminService) SetAssignments(adminId int, inboundIds []int) error {
	if _, err := s.load(adminId); err != nil {
		return err
	}
	seen := make(map[int]bool, len(inboundIds))
	unique := make([]int, 0, len(inboundIds))
	for _, id := range inboundIds {
		if id > 0 && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	db := database.GetDB()
	if len(unique) > 0 {
		var found int64
		if err := db.Model(&model.Inbound{}).Where("id IN ?", unique).Count(&found).Error; err != nil {
			return err
		}
		if int(found) != len(unique) {
			return ErrInboundNotFound
		}
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("admin_id = ?", adminId).Delete(&model.AdminInbound{}).Error; err != nil {
			return err
		}
		for _, id := range unique {
			if err := tx.Create(&model.AdminInbound{AdminId: adminId, InboundId: id}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
