package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

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
