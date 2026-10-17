package controller

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Who sees everything and who sees only their own. A manager oversees the
// resellers, so their view is unscoped: scoping them would hide the very rows
// they are there to watch. A reseller and a readonly admin are narrowed to
// their own, and scope 0 is what every list and guard reads as "unscoped".
func TestScopeForByRole(t *testing.T) {
	cases := []struct {
		role model.Role
		want int
	}{
		{model.RoleSuperAdmin, 0},
		{model.RoleManager, 0},
		{model.RoleReseller, 7},
		{model.RoleReadOnly, 7},
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			got := scopeFor(&model.User{Id: 7, Role: tc.role})
			if got != tc.want {
				t.Fatalf("scopeFor(%s) = %d, want %d", tc.role, got, tc.want)
			}
		})
	}
	if got := scopeFor(nil); got != 0 {
		t.Fatalf("scopeFor(nil) = %d, want 0", got)
	}
}
