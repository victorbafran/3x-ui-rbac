// Package authz decides which panel users may call which API routes.
// Unknown routes are denied: a route added upstream is closed until a rule
// admits it, so a rebase cannot silently open a hole.
package authz

import (
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Scope says how ownership is checked once the role gate passes.
type Scope int

const (
	// ScopeGlobal is a resource nobody owns; the role gate is the whole check.
	ScopeGlobal Scope = iota
	// ScopeInbound reads an inbound id from Param and requires the caller to own it.
	ScopeInbound
	// ScopeClient reads a client email from Param and requires the caller to own
	// every inbound that client sits on.
	ScopeClient
	// ScopeOwnedListing returns many resources; the handler's service must filter
	// them by owner, because the targets are in the body or implied, not the path.
	ScopeOwnedListing
)

// Rule is the decision for one route.
type Rule struct {
	Roles []model.Role
	Scope Scope
	Param string
}

// Role sets, widest last. Readonly may read but never write.
var (
	Admins    = []model.Role{model.RoleSuperAdmin}
	Managers  = []model.Role{model.RoleSuperAdmin, model.RoleManager}
	Operators = []model.Role{model.RoleSuperAdmin, model.RoleManager, model.RoleReseller}
	Readers   = []model.Role{model.RoleSuperAdmin, model.RoleManager, model.RoleReseller, model.RoleReadOnly}
)

// Allows reports whether role may pass this rule's role gate.
func (r Rule) Allows(role model.Role) bool {
	return slices.Contains(r.Roles, role)
}

// RuleFor resolves a route to its rule. An exact entry wins over a prefix
// entry; with neither, ok is false and the caller must deny.
func RuleFor(method, path string) (Rule, bool) {
	if rule, ok := exactRules[method+" "+path]; ok {
		return rule, true
	}
	best := -1
	var found Rule
	for _, p := range prefixRules {
		if strings.HasPrefix(path, p.Prefix) && len(p.Prefix) > best {
			best, found = len(p.Prefix), p.Rule
		}
	}
	return found, best >= 0
}

// ExactRoutes lists every exactly-matched route key. The route contract test
// uses it to catch entries left behind by a renamed or removed endpoint.
func ExactRoutes() []string {
	keys := make([]string, 0, len(exactRules))
	for k := range exactRules {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
