package controller

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// Client emails are unique across the whole panel, so two resellers could not
// both have a customer called "ali" -- and the create path would quietly reuse
// the first one. Each reseller's clients are therefore stored under a prefix
// carrying their admin id. The panel strips it for display; everything else,
// xray included, uses the stored value.
var ownerPrefixRe = regexp.MustCompile(`^a[0-9]+:`)

func ownerPrefix(adminId int) string { return fmt.Sprintf("a%d:", adminId) }

// prefixedEmail is email under adminId's namespace. An already-prefixed value
// is left alone so an edit round-trip cannot double it.
func prefixedEmail(adminId int, email string) string {
	email = strings.TrimSpace(email)
	if adminId <= 0 || email == "" || ownerPrefixRe.MatchString(email) {
		return email
	}
	return ownerPrefix(adminId) + email
}

// applyOwnerPrefix rewrites the emails a scoped admin is creating.
func (a *ClientController) applyOwnerPrefix(c *gin.Context, payloads []service.ClientCreatePayload) {
	ownerId := ownerScopeOf(c)
	if ownerId == 0 {
		return
	}
	for i := range payloads {
		payloads[i].Client.Email = prefixedEmail(ownerId, payloads[i].Client.Email)
	}
}

// stampOwner records who created these clients, once they exist. Rows that
// already have an owner are left alone, so this can never take a client from
// another admin. A client whose stamp fails stays unowned: the super_admin
// sees it and nobody else does, which is the safe way to fail.
func stampOwner(c *gin.Context, emails ...string) {
	ownerId := ownerScopeOf(c)
	if ownerId == 0 || len(emails) == 0 {
		return
	}
	database.GetDB().Model(&model.ClientRecord{}).
		Where("email IN ? AND admin_id = ?", emails, 0).
		Update("admin_id", ownerId)
}

func emailsOf(payloads []service.ClientCreatePayload) []string {
	out := make([]string, 0, len(payloads))
	for i := range payloads {
		if payloads[i].Client.Email != "" {
			out = append(out, payloads[i].Client.Email)
		}
	}
	return out
}
