// Package authz decides which panel admins may call which API routes.
// Unknown routes are denied: a route added upstream is closed until a rule
// admits it, so a rebase cannot silently open a hole.
package authz

import (
	"slices"
	"strings"
)

// PermSuperOnly marks a route no switch can open. It is deliberately absent
// from AllPermissions, so no template grants it and no override can add it.
const PermSuperOnly = "super_admin.only"

// Scope says how ownership is checked once the permission gate passes.
type Scope int

const (
	// ScopeGlobal is a resource nobody owns; the permission gate is the whole check.
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

// Rule is the decision for one route. Perm is the switch a non-super_admin
// must hold; "" admits any enabled admin and leaves scoping to the handler.
type Rule struct {
	Perm  string
	Scope Scope
	Param string
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
