package controller

import (
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
)

// ownerScopeOf is the admin id a listing must be narrowed to, or 0 when the
// caller sees every row. Only super_admin is unscoped.
func ownerScopeOf(c *gin.Context) int {
	return scopeFor(authz.Actor(c))
}

// scopeFor is ownerScopeOf for a user already in hand.
func scopeFor(u *model.User) int {
	if u == nil || u.Role == model.RoleSuperAdmin {
		return 0
	}
	return u.Id
}
