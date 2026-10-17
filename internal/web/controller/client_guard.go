package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// The authz gate checks ownership of a target named in the URL. These helpers
// cover the client routes whose targets arrive in the body or come back as a
// collection: the handler refuses foreign targets before acting, and strips
// other admins' clients before answering. A super_admin is never scoped.

const foreignMsg = "resource belongs to another admin"

func refuseForeign(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "msg": foreignMsg})
}

// denyForeign reports true, and has already answered, when the body names an
// inbound or a client the caller may not touch.
func (a *ClientController) denyForeign(c *gin.Context, inboundIds []int, emails []string) bool {
	ownerId := ownerScopeOf(c)
	if ownerId == 0 {
		return false
	}
	foreignIds, err := a.clientService.ForeignInbounds(ownerId, inboundIds)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return true
	}
	foreignEmails, err := a.clientService.ForeignEmails(ownerId, emails)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return true
	}
	if len(foreignIds) > 0 || len(foreignEmails) > 0 {
		refuseForeign(c)
		return true
	}
	return false
}

func (a *ClientController) denyForeignSubId(c *gin.Context, subId string) bool {
	ownerId := ownerScopeOf(c)
	if ownerId == 0 {
		return false
	}
	foreign, err := a.clientService.ForeignSubId(ownerId, subId)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return true
	}
	if foreign {
		refuseForeign(c)
	}
	return foreign
}

func (a *ClientController) denyForeignClientId(c *gin.Context, clientId int) bool {
	ownerId := ownerScopeOf(c)
	if ownerId == 0 {
		return false
	}
	foreign, err := a.clientService.ForeignClientId(ownerId, clientId)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return true
	}
	if foreign {
		refuseForeign(c)
	}
	return foreign
}

// ownedEmailFilter returns the caller's client emails, or nil when the caller
// is unscoped and nothing should be filtered.
func (a *ClientController) ownedEmailFilter(c *gin.Context) map[string]bool {
	ownerId := ownerScopeOf(c)
	if ownerId == 0 {
		return nil
	}
	set, err := a.clientService.OwnedEmailSet(ownerId)
	if err != nil {
		return map[string]bool{}
	}
	return set
}

// ownedTagFilter is ownedEmailFilter for inbound tags.
func (a *ClientController) ownedTagFilter(c *gin.Context) map[string]bool {
	ownerId := ownerScopeOf(c)
	if ownerId == 0 {
		return nil
	}
	set, err := a.clientService.OwnedInboundTagSet(ownerId)
	if err != nil {
		return map[string]bool{}
	}
	return set
}

func keepEmails(list []string, owned map[string]bool) []string {
	if owned == nil {
		return list
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if owned[e] {
			out = append(out, e)
		}
	}
	return out
}

func keepEmailsByGuid(m map[string][]string, owned map[string]bool) map[string][]string {
	if owned == nil {
		return m
	}
	out := make(map[string][]string, len(m))
	for guid, emails := range m {
		out[guid] = keepEmails(emails, owned)
	}
	return out
}

func keepEmailKeys[T any](m map[string]T, owned map[string]bool) map[string]T {
	if owned == nil {
		return m
	}
	out := make(map[string]T, len(m))
	for email, v := range m {
		if owned[email] {
			out[email] = v
		}
	}
	return out
}

func keepEmailKeysByGuid[T any](m map[string]map[string]T, owned map[string]bool) map[string]map[string]T {
	if owned == nil {
		return m
	}
	out := make(map[string]map[string]T, len(m))
	for guid, inner := range m {
		out[guid] = keepEmailKeys(inner, owned)
	}
	return out
}

func inboundIdsOf(payloads []service.ClientCreatePayload) []int {
	var ids []int
	for i := range payloads {
		ids = append(ids, payloads[i].InboundIds...)
	}
	return ids
}

// denyUnlimited refuses a client with no data limit or no expiry from a caller
// whose switches do not allow handing those out. Only an exact zero is
// unlimited: a negative expiry is a delayed start and stays allowed.
func (a *ClientController) denyUnlimited(c *gin.Context, clients ...model.Client) bool {
	actor := authz.Actor(c)
	if actor == nil || actor.Role == model.RoleSuperAdmin {
		return false
	}
	perms := authz.Effective(actor)
	for i := range clients {
		if clients[i].TotalGB == 0 && !perms.Has(authz.ClientUnlimitedData) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "msg": "missing permission: " + authz.ClientUnlimitedData})
			return true
		}
		if clients[i].ExpiryTime == 0 && !perms.Has(authz.ClientUnlimitedExpiry) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "msg": "missing permission: " + authz.ClientUnlimitedExpiry})
			return true
		}
	}
	return false
}

func clientsOf(payloads []service.ClientCreatePayload) []model.Client {
	out := make([]model.Client, 0, len(payloads))
	for i := range payloads {
		out = append(out, payloads[i].Client)
	}
	return out
}

// denyOverUsersLimit stops a reseller with a client limit from creating past
// it. Clients they own, plus the ones in this request, must fit under it.
func (a *ClientController) denyOverUsersLimit(c *gin.Context, adding int) bool {
	actor := authz.Actor(c)
	if actor == nil || actor.Role == model.RoleSuperAdmin || actor.UsersLimit <= 0 || adding <= 0 {
		return false
	}
	owned, err := a.clientService.OwnedEmailSet(actor.Id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return true
	}
	if int64(len(owned)+adding) > actor.UsersLimit {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "msg": "users limit reached"})
		return true
	}
	return false
}

// fillOwnedInbounds supplies the caller's own inbounds when the request names
// none. A reseller whose super_admin took the inbounds page away cannot pick
// one, so what they were assigned stands in for the choice. An unscoped
// caller, or a request that already names inbounds, is left alone.
func (a *ClientController) fillOwnedInbounds(c *gin.Context, ids []int) []int {
	ownerId := ownerScopeOf(c)
	if ownerId == 0 || len(ids) > 0 {
		return ids
	}
	owned, err := a.clientService.OwnedInboundIds(ownerId)
	if err != nil {
		return ids
	}
	return owned
}

func (a *ClientController) fillOwnedInboundsAll(c *gin.Context, payloads []service.ClientCreatePayload) {
	for i := range payloads {
		payloads[i].InboundIds = a.fillOwnedInbounds(c, payloads[i].InboundIds)
	}
}
