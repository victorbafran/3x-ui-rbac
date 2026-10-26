package service

import (
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

// ownedClientsCond restricts a query over "clients AS c" to the clients this
// admin created. Deriving it from the inbound instead handed every reseller on
// a shared inbound the whole customer list, and the right to edit it.
const ownedClientsCond = `c.admin_id = ?`

// OwnedEmailsAmong is OwnedEmailSet for many admins at once, asked only about emails:
// a push names a few clients, and loading each admin's whole list for it does not scale.
func (s *ClientService) OwnedEmailsAmong(ownerIds []int, emails []string) (map[int]map[string]bool, error) {
	byAdmin := make(map[int]map[string]bool, len(ownerIds))
	emails = uniqueNonEmptyStrings(emails)
	if len(ownerIds) == 0 || len(emails) == 0 {
		return byAdmin, nil
	}
	for _, batch := range chunkStrings(emails, max(sqliteMaxVars-len(ownerIds), 1)) {
		var rows []struct {
			AdminId int
			Email   string
		}
		// ownedClientsCond, for every admin in ownerIds.
		if err := database.GetDB().Table("clients AS c").Select("c.admin_id, c.email").
			Where("c.admin_id IN ?", ownerIds).Where("c.email IN ?", batch).
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			if byAdmin[r.AdminId] == nil {
				byAdmin[r.AdminId] = make(map[string]bool)
			}
			byAdmin[r.AdminId][r.Email] = true
		}
	}
	return byAdmin, nil
}

// scopeToOwner applies the ownership restriction when ownerId is set. An
// ownerId of 0 means an unscoped caller, such as a super_admin.
func scopeToOwner(tx *gorm.DB, ownerId int) *gorm.DB {
	if ownerId <= 0 {
		return tx
	}
	return tx.Where(ownedClientsCond, ownerId)
}

// ListForOwner is List narrowed to one admin's clients.
func (s *ClientService) ListForOwner(ownerId int) ([]ClientWithAttachments, error) {
	if ownerId <= 0 {
		return s.List()
	}
	var ids []int
	err := scopeToOwner(database.GetDB().Table("clients AS c"), ownerId).
		Pluck("c.id", &ids).Error
	if err != nil {
		return nil, err
	}
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	keep := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		keep[id] = struct{}{}
	}
	out := make([]ClientWithAttachments, 0, len(ids))
	for i := range all {
		if _, ok := keep[all[i].Id]; ok {
			out = append(out, all[i])
		}
	}
	return out, nil
}
