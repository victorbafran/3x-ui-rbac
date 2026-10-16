package database

import "gorm.io/gorm"

// OwnedInboundCond is the one rule for "this admin may touch this inbound":
// they created it, or a super_admin assigned it to them. Bind ownerId twice.
func OwnedInboundCond(alias string) string {
	return "(" + alias + ".user_id = ? OR " + alias + ".id IN (SELECT inbound_id FROM admin_inbounds WHERE admin_id = ?))"
}

// OwnedInbounds scopes an inbounds query to one admin. ownerId <= 0 is the
// super_admin path and leaves the query untouched.
func OwnedInbounds(ownerId int) func(*gorm.DB) *gorm.DB {
	return func(tx *gorm.DB) *gorm.DB {
		if ownerId <= 0 {
			return tx
		}
		return tx.Where(OwnedInboundCond("inbounds"), ownerId, ownerId)
	}
}
