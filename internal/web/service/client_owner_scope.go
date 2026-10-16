package service

import (
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

// ownedClientsCond restricts a query over "clients AS c" to clients on inbounds
// the admin created or was assigned. EXISTS rather than a join, so a client on
// several inbounds is not counted twice. Binds ownerId twice.
var ownedClientsCond = `EXISTS (
	SELECT 1 FROM client_inbounds ci
	JOIN inbounds i ON i.id = ci.inbound_id
	WHERE ci.client_id = c.id AND ` + database.OwnedInboundCond("i") + `
)`

// scopeToOwner applies the ownership restriction when ownerId is set. An
// ownerId of 0 means an unscoped caller, such as a super_admin.
func scopeToOwner(tx *gorm.DB, ownerId int) *gorm.DB {
	if ownerId <= 0 {
		return tx
	}
	return tx.Where(ownedClientsCond, ownerId, ownerId)
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
