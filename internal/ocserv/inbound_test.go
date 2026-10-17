package ocserv

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func ciscoInbound(settings string) *model.Inbound {
	return &model.Inbound{Id: 4, Tag: "cisco-4", Port: 443, Protocol: model.Cisco, Settings: settings}
}

func TestInstanceFromInboundServesOnlyEnabledClients(t *testing.T) {
	inst, ok := InstanceFromInbound(ciscoInbound(`{"clients":[
		{"email":"ann@example.com","password":"annpass","enable":true},
		{"email":"bob@example.com","password":"bobpass","enable":false},
		{"email":"nopass@example.com","password":"","enable":true}
	]}`))
	if !ok {
		t.Fatal("a cisco inbound with one usable client was not served")
	}
	if got := inst.UserNames(); len(got) != 1 || got[0] != "ann@example.com" {
		t.Fatalf("served %v, want only the enabled client with a password", got)
	}
}

// A reseller's client email carries a colon that ocpasswd cannot hold. The
// email stays the billing identity; only the login name is rewritten.
func TestInstanceFromInboundDerivesAWritableLogin(t *testing.T) {
	inst, ok := InstanceFromInbound(ciscoInbound(`{"clients":[
		{"email":"a9:customer@example.com","password":"pw","enable":true}
	]}`))
	if !ok {
		t.Fatal("a reseller's client was dropped entirely")
	}
	u := inst.Users[0]
	if u.Name != "a9:customer@example.com" {
		t.Fatalf("the billing identity changed to %q", u.Name)
	}
	if err := ValidLogin(u.Login); err != nil {
		t.Fatalf("the derived login is unusable: %v", err)
	}
}

// Two emails can collapse onto one login once the colon is substituted. Two
// clients sharing a login would share one account, so the second is refused.
func TestInstanceFromInboundRefusesASecondClientOnTheSameLogin(t *testing.T) {
	inst, ok := InstanceFromInbound(ciscoInbound(`{"clients":[
		{"email":"a9:bob@example.com","password":"first","enable":true},
		{"email":"a9_bob@example.com","password":"second","enable":true}
	]}`))
	if !ok {
		t.Fatal("the colliding pair took the whole inbound down")
	}
	if len(inst.Users) != 1 {
		t.Fatalf("both colliding clients were served: %+v", inst.Users)
	}
	if inst.Users[0].Password != "first" {
		t.Fatalf("the second client took over the login; password is %q", inst.Users[0].Password)
	}
}

// An inbound nobody can log into is a port held open for nothing, and
// returning it would keep an ocserv running with an empty password file.
func TestInstanceFromInboundSkipsAnInboundWithNoUsableClient(t *testing.T) {
	if _, ok := InstanceFromInbound(ciscoInbound(`{"clients":[]}`)); ok {
		t.Fatal("an inbound with no clients was served")
	}
	if _, ok := InstanceFromInbound(ciscoInbound(`{"clients":[{"email":"a","password":"p","enable":false}]}`)); ok {
		t.Fatal("an inbound whose only client is disabled was served")
	}
}

func TestInstanceFromInboundIgnoresOtherProtocols(t *testing.T) {
	ib := ciscoInbound(`{"clients":[{"email":"a","password":"p","enable":true}]}`)
	ib.Protocol = model.VLESS
	if _, ok := InstanceFromInbound(ib); ok {
		t.Fatal("a vless inbound was handed to the ocserv sidecar")
	}
	if _, ok := InstanceFromInbound(nil); ok {
		t.Fatal("a nil inbound was served")
	}
}

func TestInstanceFromInboundReadsTheNetworkSettings(t *testing.T) {
	inst, ok := InstanceFromInbound(ciscoInbound(`{
		"clients":[{"email":"ann","password":"p","enable":true}],
		"network":"10.99.0.0","netmask":"255.255.255.0",
		"dns":["9.9.9.9",""," 1.1.1.1 "],
		"maxClients":50,"maxSameClients":3
	}`))
	if !ok {
		t.Fatal("the inbound was not served")
	}
	if inst.CIDR() != "10.99.0.0/24" {
		t.Fatalf("pool = %s, want the configured 10.99.0.0/24", inst.CIDR())
	}
	if len(inst.DNS) != 2 || inst.DNS[0] != "9.9.9.9" || inst.DNS[1] != "1.1.1.1" {
		t.Fatalf("dns = %v, want the two non-empty entries trimmed", inst.DNS)
	}
	if inst.MaxClients != 50 || inst.MaxSameClients != 3 {
		t.Fatalf("limits = %d/%d, want 50/3", inst.MaxClients, inst.MaxSameClients)
	}
}
