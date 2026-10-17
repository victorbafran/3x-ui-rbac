package authz

import "testing"

// The panel can be served under a secret base path, so the router registers
// every route beneath it. Matching rules against the raw path then finds
// nothing and the middleware denies the whole API, for every role.
func TestCanonicalPathStripsPanelBasePath(t *testing.T) {
	cases := []struct {
		name    string
		apiBase string
		full    string
		want    string
	}{
		{"default base", "/panel/api", "/panel/api/inbounds/list", "/panel/api/inbounds/list"},
		{"secret base", "/AljDAVj4ffm7/panel/api", "/AljDAVj4ffm7/panel/api/inbounds/list", "/panel/api/inbounds/list"},
		{"secret base with param", "/AljDAVj4ffm7/panel/api", "/AljDAVj4ffm7/panel/api/inbounds/del/:id", "/panel/api/inbounds/del/:id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canonicalPath(tc.apiBase, tc.full); got != tc.want {
				t.Fatalf("canonicalPath(%q, %q) = %q, want %q", tc.apiBase, tc.full, got, tc.want)
			}
		})
	}
}

// The end the user actually hit: a real route under a secret base path must
// resolve to its rule, not fall through to the deny-by-default branch.
func TestRuleForResolvesUnderSecretBasePath(t *testing.T) {
	const apiBase = "/AljDAVj4ffm7/panel/api"
	for _, full := range []string{
		"/AljDAVj4ffm7/panel/api/inbounds/list",
		"/AljDAVj4ffm7/panel/api/server/status",
		"/AljDAVj4ffm7/panel/api/clients/list",
	} {
		if _, ok := RuleFor("GET", canonicalPath(apiBase, full)); !ok {
			t.Fatalf("no rule found for %q; the whole API would be denied", full)
		}
	}
}
