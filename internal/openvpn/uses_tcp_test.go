package openvpn

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The customer's own file has to name the transport the server listens on. A
// profile that says udp against a tcp server does not report a mismatch; it
// waits until it times out, which reads as a dead server.
func TestUsesTCPReadsTheInboundsOwnSetting(t *testing.T) {
	for _, tc := range []struct {
		name     string
		inbound  *model.Inbound
		want     bool
		whyWrong string
	}{
		{
			name: "a tcp inbound",
			inbound: &model.Inbound{
				Protocol: model.OpenVPN,
				Settings: `{"useTcp":true,"clients":[]}`,
			},
			want:     true,
			whyWrong: "the customer's file would say udp and never connect",
		},
		{
			name: "a udp inbound",
			inbound: &model.Inbound{
				Protocol: model.OpenVPN,
				Settings: `{"useTcp":false,"clients":[]}`,
			},
			want:     false,
			whyWrong: "the customer's file would say tcp against a udp server",
		},
		{
			name: "settings that do not mention it",
			inbound: &model.Inbound{
				Protocol: model.OpenVPN,
				Settings: `{"clients":[]}`,
			},
			want:     false,
			whyWrong: "udp is the default the server itself uses",
		},
		{
			name: "settings that are not json",
			inbound: &model.Inbound{
				Protocol: model.OpenVPN,
				Settings: `{"useTcp":true`,
			},
			want:     false,
			whyWrong: "a broken inbound should not claim a transport it cannot prove",
		},
		{
			name: "another protocol's inbound",
			inbound: &model.Inbound{
				Protocol: model.Cisco,
				Settings: `{"useTcp":true,"clients":[]}`,
			},
			want:     false,
			whyWrong: "a cisco inbound's settings mean something else entirely",
		},
		{
			name:     "no inbound at all",
			inbound:  nil,
			want:     false,
			whyWrong: "a nil inbound must not panic the subscription page",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := UsesTCP(tc.inbound); got != tc.want {
				t.Fatalf("UsesTCP = %v, want %v: %s", got, tc.want, tc.whyWrong)
			}
		})
	}
}
