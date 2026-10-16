package authz

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

// ActorKey holds the authorized caller for handlers and services that must
// filter a collection themselves (ScopeOwnedListing).
const ActorKey = "authz_actor"

// apiPrefix is the path every registry key starts with.
const apiPrefix = "/panel/api"

var (
	errBadParam  = errors.New("malformed resource identifier")
	errNotOwner  = errors.New("resource belongs to another admin")
	errNoClient  = errors.New("client not found")
	errNoInbound = errors.New("inbound not found")
)

// Actor returns the caller a passed Middleware stored, if any.
func Actor(c *gin.Context) *model.User {
	if v, ok := c.Get(ActorKey); ok {
		if u, ok := v.(*model.User); ok {
			return u
		}
	}
	return nil
}

// canonicalPath rewrites a matched route to the "/panel/api/..." form the
// registry is keyed by. The panel may be served under a secret base path, and
// every route is registered beneath it.
func canonicalPath(apiBase, fullPath string) string {
	if apiBase == "" || !strings.HasPrefix(fullPath, apiBase) {
		return fullPath
	}
	return apiPrefix + strings.TrimPrefix(fullPath, apiBase)
}

// Middleware denies any API request whose route has no rule, whose caller
// lacks the rule's switch, or whose target the caller does not own. A
// super_admin passes every check; a disabled account passes none.
func Middleware(apiBase string) gin.HandlerFunc {
	return func(c *gin.Context) {
		rule, found := RuleFor(c.Request.Method, canonicalPath(apiBase, c.FullPath()))
		if !found {
			abort(c, http.StatusForbidden, "route is not covered by an authorization rule")
			return
		}
		user := session.GetLoginUser(c)
		if user == nil {
			abort(c, http.StatusUnauthorized, "not signed in")
			return
		}
		if !user.Enabled {
			abort(c, http.StatusForbidden, "account is disabled")
			return
		}
		if user.Role != model.RoleSuperAdmin {
			if rule.Perm != "" && !Effective(user).Has(rule.Perm) {
				abort(c, http.StatusForbidden, "missing permission: "+rule.Perm)
				return
			}
			if err := checkOwnership(c, rule, user); err != nil {
				abort(c, http.StatusForbidden, err.Error())
				return
			}
		}
		c.Set(ActorKey, user)
		c.Next()
	}
}

func checkOwnership(c *gin.Context, rule Rule, user *model.User) error {
	switch rule.Scope {
	case ScopeInbound:
		id, err := strconv.Atoi(c.Param(rule.Param))
		if err != nil {
			return errBadParam
		}
		return ownsInbound(user.Id, id)
	case ScopeClient:
		return ownsClient(user.Id, c.Param(rule.Param))
	default:
		return nil
	}
}

// ownsInbound passes when the inbound exists and the admin created it or was
// assigned it. Both are decided by the one shared condition.
func ownsInbound(userId, inboundId int) error {
	db := database.GetDB()
	var exists int64
	if err := db.Model(&model.Inbound{}).Where("id = ?", inboundId).Count(&exists).Error; err != nil || exists == 0 {
		return errNoInbound
	}
	var owned int64
	err := db.Model(&model.Inbound{}).Where("id = ?", inboundId).
		Scopes(database.OwnedInbounds(userId)).Count(&owned).Error
	if err != nil {
		return err
	}
	if owned == 0 {
		return errNotOwner
	}
	return nil
}

// ownsClient requires every inbound the client sits on to be the admin's. A
// client shared onto someone else's inbound is not this admin's to touch.
func ownsClient(userId int, email string) error {
	if email == "" {
		return errBadParam
	}
	db := database.GetDB()
	var inboundIds []int
	err := db.Model(&model.ClientInbound{}).
		Joins("JOIN clients ON clients.id = client_inbounds.client_id").
		Where("clients.email = ?", email).
		Pluck("client_inbounds.inbound_id", &inboundIds).Error
	if err != nil || len(inboundIds) == 0 {
		return errNoClient
	}
	var owned int64
	err = db.Model(&model.Inbound{}).Where("id IN ?", inboundIds).
		Scopes(database.OwnedInbounds(userId)).Count(&owned).Error
	if err != nil {
		return err
	}
	if int(owned) != len(inboundIds) {
		return errNotOwner
	}
	return nil
}

func abort(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"success": false, "msg": msg})
}
