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

// Middleware denies any API request whose route has no rule, whose caller's
// role the rule excludes, or whose target resource the caller does not own.
// apiBase is the API group's own base path, which carries the panel's base
// path with it.
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
		if !rule.Allows(user.Role) {
			abort(c, http.StatusForbidden, "role may not call this endpoint")
			return
		}
		// A super_admin owns everything, so per-resource lookups are skipped.
		if user.Role != model.RoleSuperAdmin {
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

// ownsInbound reports whether inbound id carries this user's owner tag.
func ownsInbound(userId, inboundId int) error {
	db := database.GetDB()
	var ownerId int
	err := db.Model(&model.Inbound{}).
		Where("id = ?", inboundId).
		Pluck("user_id", &ownerId).Error
	if err != nil {
		return errNoInbound
	}
	if ownerId != userId {
		return errNotOwner
	}
	return nil
}

// ownsClient requires every inbound the client sits on to be this user's. A
// client shared onto someone else's inbound is not this user's to touch.
func ownsClient(userId int, email string) error {
	if email == "" {
		return errBadParam
	}
	db := database.GetDB()
	var ownerIds []int
	err := db.Model(&model.ClientInbound{}).
		Joins("JOIN clients ON clients.id = client_inbounds.client_id").
		Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id").
		Where("clients.email = ?", email).
		Pluck("inbounds.user_id", &ownerIds).Error
	if err != nil {
		return errNoClient
	}
	if len(ownerIds) == 0 {
		return errNoClient
	}
	for _, id := range ownerIds {
		if id != userId {
			return errNotOwner
		}
	}
	return nil
}

func abort(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"success": false, "msg": msg})
}
