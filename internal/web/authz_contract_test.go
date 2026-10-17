package web

import (
	"sort"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
)

// Every /panel/api route must resolve to an authz rule. A route with no rule is
// denied at runtime, so this failing means an endpoint is unreachable -- and a
// route that reaches a handler with no rule at all would be unguarded.
func TestEveryAPIRouteHasAnAuthzRule(t *testing.T) {
	var unruled []string
	for route := range registeredContractRoutes(t) {
		method, path, ok := strings.Cut(route, " ")
		if !ok || !strings.HasPrefix(path, "/panel/api/") {
			continue
		}
		if _, found := authz.RuleFor(method, path); !found {
			unruled = append(unruled, route)
		}
	}
	if len(unruled) > 0 {
		sort.Strings(unruled)
		t.Fatalf("%d API route(s) have no authz rule:\n  %s", len(unruled), strings.Join(unruled, "\n  "))
	}
}

// An exact rule for a route that no longer exists is dead weight that reads as
// coverage: the next reviewer sees the endpoint listed and assumes it is gated.
func TestNoStaleAuthzRules(t *testing.T) {
	live := registeredContractRoutes(t)
	var stale []string
	for _, key := range authz.ExactRoutes() {
		if !live[key] {
			stale = append(stale, key)
		}
	}
	if len(stale) > 0 {
		t.Fatalf("%d authz rule(s) name a route that is not registered:\n  %s", len(stale), strings.Join(stale, "\n  "))
	}
}
