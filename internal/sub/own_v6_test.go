package sub

import (
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const (
	ownName4   = "vpn.example.com"
	ownName6   = "ipv6.example.com"
	ownLiteral = "2001:db8::5"
)

// ownInbound is a cisco or OpenVPN inbound customers reach at ownName4, with
// the IPv6 settings given as JSON members.
func ownInbound(protocol model.Protocol, port int, v6 string) *model.Inbound {
	return behindFront(protocol, port, `{"clients":[]`+v6+`}`, `{}`, ownName4)
}

// An inbound's own IPv6 is served by this server, not a front: "v6" sends the
// customer there, "both" lists it first and keeps today's address after it.
func TestOwnIPv6CiscoLoginsFollowTheFamily(t *testing.T) {
	for _, tc := range []struct {
		name string
		v6   string
		mode string
		want []CiscoLogin
	}{
		{"IPv4 as before", ``, "", []CiscoLogin{{Server: ownName4}}},
		{"family without an address", `,"ipFamily":"v6"`, "", []CiscoLogin{{Server: ownName4}}},
		{"v6 by name", `,"ipFamily":"v6","address6":"` + ownName6 + `"`, "", []CiscoLogin{{Server: ownName6}}},
		{"v6 literal", `,"ipFamily":"v6","address6":"` + ownLiteral + `"`, "", []CiscoLogin{{Server: "[" + ownLiteral + "]"}}},
		{"both", `,"ipFamily":"both","address6":"` + ownName6 + `"`, "", []CiscoLogin{
			{Server: ownName6, Label: "IPv6", Family: familyV6}, {Server: ownName4, Label: "IPv4", Family: familyV4},
		}},
		{"both numbered", `,"ipFamily":"both","address6":"` + ownName6 + `"`, "number", []CiscoLogin{
			{Server: ownName6, Label: "1"}, {Server: ownName4, Label: "2"},
		}},
		{"both unmarked", `,"ipFamily":"both","address6":"` + ownName6 + `"`, "off", []CiscoLogin{
			{Server: ownName6}, {Server: ownName4},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &SubService{remarkIPFamily: tc.mode}
			for i := range tc.want {
				tc.want[i].Email, tc.want[i].Port, tc.want[i].Username, tc.want[i].Password = "user", 4443, "user", "pw"
			}
			got := s.ciscoLoginsFor(ownInbound(model.Cisco, 4443, tc.v6), model.Client{Email: "user", Password: "pw"})
			if !slices.Equal(got, tc.want) {
				t.Fatalf("cisco logins\n got: %+v\nwant: %+v", got, tc.want)
			}
		})
	}
}

// A front handing out IPv6 is where the operator sent these customers; the
// inbound's own IPv6 would take them around it, so the front decides.
func TestAnIPv6FrontOutranksTheInboundsOwnIPv6(t *testing.T) {
	s := requestFor(t, testFront("v6"))
	inbound := behindFront(model.Cisco, 4443, `{"ipFamily":"both","address6":"`+ownName6+`"}`, `{}`, testFrontName)
	got := s.ciscoLoginsFor(inbound, model.Client{Email: "user", Password: "pw"})
	if len(got) != 1 || got[0].Server != "["+testFrontIPv6+"]" {
		t.Fatalf("cisco logins = %+v, want the front's IPv6 alone", got)
	}
}

// One file either way: "v6" dials the IPv6, "both" puts it as a remote ahead
// of today's address, which the client falls back to.
func TestOwnIPv6OpenVPNProfileFollowsTheFamily(t *testing.T) {
	old := localCountryLabel
	localCountryLabel = func() string { return "" }
	t.Cleanup(func() { localCountryLabel = old })

	for _, tc := range []struct {
		name              string
		v6                string
		address, address6 string
	}{
		{"IPv4 as before", ``, ownName4, ""},
		{"v6 by name", `,"ipFamily":"v6","address6":"` + ownName6 + `"`, ownName6, ""},
		{"v6 literal", `,"ipFamily":"v6","address6":"[` + ownLiteral + `]"`, "[" + ownLiteral + "]", ""},
		{"both", `,"ipFamily":"both","address6":"` + ownName6 + `"`, ownName4, ownName6},
		{"an IPv4 literal is no IPv6", `,"ipFamily":"both","address6":"203.0.113.9"`, ownName4, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &SubService{}
			profile, ok := s.openvpnProfileFor(ownInbound(model.OpenVPN, 1194, tc.v6), model.Client{Email: "user", Password: "pw"})
			if !ok || profile.Address != tc.address || profile.Address6 != tc.address6 {
				t.Fatalf("profile remotes = %q then %q, want %q then %q", profile.Address6, profile.Address, tc.address6, tc.address)
			}
		})
	}
}
