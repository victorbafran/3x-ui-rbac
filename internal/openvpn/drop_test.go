package openvpn

import (
	"os/user"
	"strings"
	"testing"
)

// The bug this file exists for: openvpn was told to drop to nobody, and the
// credentials file was written root-only on the belief that the panel would
// be the one reading it. The helper is spawned by openvpn *after* the drop,
// so it read the file as nobody, the open failed, and every customer was
// told their password was wrong.
//
// Nothing in the generated config showed it. Both halves were individually
// reasonable; only their combination locked everyone out. The ownership half
// is checked in drop_linux_test.go, where the ownership of a file is a thing
// that exists.

func TestDroppingPrivilegesAndTheFileGroupAreOneDecision(t *testing.T) {
	// Offered an account, the config drops to it.
	cfg := configFor(t, Instance{Id: 1, Port: 1194})
	if !strings.Contains(cfg, "user nobody") || !strings.Contains(cfg, "group nogroup") {
		t.Fatalf("the config does not drop privileges, though an account was offered:\n%s", cfg)
	}

	// Offered none, it must not name one. openvpn refuses to start on a user
	// it cannot resolve, which takes the inbound down rather than merely
	// leaving it privileged.
	none := configWithoutDrop(t, Instance{Id: 1, Port: 1194})
	for _, line := range strings.Split(none, "\n") {
		if strings.HasPrefix(line, "user ") || strings.HasPrefix(line, "group ") {
			t.Errorf("the config says %q with no account resolved; openvpn would refuse to start:\n%s", line, none)
		}
	}
}

// ResolveDropAccount must answer with a group that exists, or with nothing.
// A name that is not on the host is the one answer that breaks the inbound.
func TestTheDropAccountIsNeverAGroupThatDoesNotExist(t *testing.T) {
	t.Cleanup(func() { lookupGroupForTest = nil })

	lookupGroupForTest = func(string) (*user.Group, bool) { return nil, false }
	if got := ResolveDropAccount(); got != nil {
		t.Fatalf("got %+v with no group on the host; openvpn would be told to drop to a group that is not there", got)
	}

	// Debian and Ubuntu name it nogroup, RHEL and Alpine name it nobody.
	// Whichever is present is the one that must come back.
	lookupGroupForTest = func(name string) (*user.Group, bool) {
		if name == "nobody" {
			return &user.Group{Name: "nobody", Gid: "99"}, true
		}
		return nil, false
	}
	got := ResolveDropAccount()
	if got == nil {
		// Only meaningful where the nobody *user* exists; a machine without
		// one correctly answers nil and there is nothing to assert.
		if _, err := lookupUser(dropUser); err == nil {
			t.Fatal("a host with only the nobody group got no drop account")
		}
		t.Skip("this machine has no nobody user, so there is no account to resolve")
	}
	if got.Group != "nobody" || got.GID != 99 {
		t.Fatalf("got %+v, want the nobody group with its real gid", got)
	}
}
