package authz

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A reseller's template must not hand out the things the owner said stay
// with the super admin: global pages, deleting customers, creating inbounds.
func TestResellerDefaultsAreNarrow(t *testing.T) {
	p := RoleDefaults(model.RoleReseller)
	for _, must := range []string{SecInbounds, SecClients, ClientCreate, ClientViewLinks} {
		if !p.Has(must) {
			t.Errorf("reseller default lacks %s", must)
		}
	}
	for _, mustNot := range []string{SecXray, SecSettings, SecAdmins, SecNodes, ClientDelete, ClientUnlimitedData, InboundCreate, InboundDelete} {
		if p.Has(mustNot) {
			t.Errorf("reseller default grants %s", mustNot)
		}
	}
}

// Overrides are the whole point: the super admin flips one switch for one
// reseller and nothing else about the template moves.
func TestEffectiveAppliesOverridesBothWays(t *testing.T) {
	u := &model.User{Role: model.RoleReseller, Permissions: `{"clients.delete":true,"sections.clients":false}`}
	p := Effective(u)
	if !p.Has(ClientDelete) {
		t.Error("override granting clients.delete was ignored")
	}
	if p.Has(SecClients) {
		t.Error("override revoking sections.clients was ignored")
	}
	if !p.Has(ClientCreate) {
		t.Error("untouched template entry clients.create was lost")
	}
}

// A key nobody defined must never read as granted, whatever the column says.
func TestUnknownKeysAreDroppedAndNeverGranted(t *testing.T) {
	u := &model.User{Role: model.RoleReseller, Permissions: `{"sections.everything":true,"root":true}`}
	p := Effective(u)
	if p.Has("sections.everything") || p.Has("root") {
		t.Fatal("unknown key read as granted")
	}
	got, err := ParseOverrides(u.Permissions)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unknown keys survived parsing: %v", got)
	}
}

// A corrupt column must degrade to the role template, not to a locked-out
// admin -- and not to a fully open one either.
func TestMalformedColumnFallsBackToTemplate(t *testing.T) {
	u := &model.User{Role: model.RoleReseller, Permissions: `{not json`}
	p := Effective(u)
	if !p.Has(ClientCreate) {
		t.Error("corrupt column locked the admin out of their template")
	}
	if p.Has(SecXray) {
		t.Error("corrupt column opened a page the template does not grant")
	}
}

// Round-trip keeps only known keys and writes them in a stable order.
func TestEncodeOverridesIsStableAndClosed(t *testing.T) {
	raw := EncodeOverrides(map[string]bool{InboundCreate: true, "bogus.key": true, ClientDelete: false})
	want := `{"clients.delete":false,"inbounds.create":true}`
	if raw != want {
		t.Fatalf("encoded %s, want %s", raw, want)
	}
	if EncodeOverrides(nil) != "" {
		t.Fatal("empty overrides must encode to the empty column, not {}")
	}
	if EncodeOverrides(map[string]bool{"bogus.key": true}) != "" {
		t.Fatal("overrides made only of unknown keys must encode to the empty column")
	}
}
