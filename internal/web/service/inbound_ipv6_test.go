package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// What a cisco or OpenVPN inbound's own IPv6 may be saved as. A family without an
// address hands customers nothing; an IPv4 literal is not an IPv6; a name is the operator's.
func TestValidateSidecarIPv6(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol model.Protocol
		settings string
		want     string // "" accepted, else a part of the error
	}{
		{"IPv4 as before", model.Cisco, `{"clients":[]}`, ""},
		{"an address kept with IPv6 off", model.OpenVPN, `{"address6":"ipv6.example.com"}`, ""},
		{"v6 by name", model.Cisco, `{"ipFamily":"v6","address6":"ipv6.example.com"}`, ""},
		{"both by literal", model.OpenVPN, `{"ipFamily":"both","address6":"2a01:4f8::1"}`, ""},
		{"bracketed literal", model.Cisco, `{"ipFamily":"both","address6":"[2a01:4f8::1]"}`, ""},
		{"v6 without an address", model.Cisco, `{"ipFamily":"v6"}`, "needs an IPv6 address"},
		{"both without an address", model.OpenVPN, `{"ipFamily":"both","address6":"  "}`, "needs an IPv6 address"},
		{"unknown family", model.Cisco, `{"ipFamily":"ipv6","address6":"ipv6.example.com"}`, `not "ipv6"`},
		{"IPv4 literal", model.OpenVPN, `{"ipFamily":"v6","address6":"203.0.113.9"}`, "is not an IPv6 address"},
		{"private literal", model.Cisco, `{"ipFamily":"v6","address6":"fd00::1"}`, "is not a public IPv6 address"},
		{"name with a space", model.OpenVPN, `{"ipFamily":"v6","address6":"ipv6 .example.com"}`, "is not a host name"},
		{"another protocol", model.VLESS, `{"ipFamily":"v6"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSidecarIPv6(tc.protocol, tc.settings)
			if tc.want == "" && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// Both save paths check it: a form or API call that skips one would store an IPv6
// customers are then handed and cannot reach.
func TestSavePathsRefuseAnIPv6FamilyWithNoAddress(t *testing.T) {
	const want = "needs an IPv6 address"
	inbound := func(port int, v6 string) *model.Inbound {
		return &model.Inbound{
			Enable: true, Port: port, Protocol: model.OpenVPN,
			Settings: `{"clients":[]` + v6 + `}`, StreamSettings: "{}", Sniffing: "{}",
		}
	}
	for _, tc := range []struct {
		name string
		save func(t *testing.T) error
	}{
		{"add inbound", func(t *testing.T) error {
			_, _, err := (&InboundService{}).AddInbound(inbound(11941, `,"ipFamily":"v6"`))
			return err
		}},
		{"update inbound", func(t *testing.T) error {
			in, _, err := (&InboundService{}).AddInbound(inbound(11942, `,"ipFamily":"both","address6":"ipv6.example.com"`))
			if err != nil {
				t.Fatalf("AddInbound: %v", err)
			}
			in.Settings = `{"clients":[],"ipFamily":"both"}`
			_, _, err = (&InboundService{}).UpdateInbound(in)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupBulkDB(t)
			if err := tc.save(t); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("save err = %v, want one containing %q", err, want)
			}
		})
	}
}
