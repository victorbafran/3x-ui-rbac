package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// cisco binds TCP plus UDP for DTLS and OpenVPN one transport, UDP by default;
// both used to read as TCP, so real clashes were saved and false ones refused.
func TestPortConflict_TheSidecarsClaimWhatTheyBind(t *testing.T) {
	cases := []struct {
		name     string
		existing *model.Inbound
		offered  *model.Inbound
		clash    bool
		port     int
	}{
		{
			name:     "hysteria beside cisco takes the DTLS socket",
			existing: &model.Inbound{Port: 443, Protocol: model.Cisco, Settings: `{"clients":[]}`},
			offered:  &model.Inbound{Port: 443, Protocol: model.Hysteria},
			clash:    true,
			port:     443,
		},
		{
			name:     "cisco beside hysteria, the other way round",
			existing: &model.Inbound{Port: 443, Protocol: model.Hysteria},
			offered:  &model.Inbound{Port: 443, Protocol: model.Cisco, Settings: `{}`},
			clash:    true,
			port:     443,
		},
		{
			name:     "with DTLS off cisco leaves UDP free",
			existing: &model.Inbound{Port: 443, Protocol: model.Cisco, Settings: `{"disableUdp":true}`},
			offered:  &model.Inbound{Port: 443, Protocol: model.Hysteria},
		},
		{
			name:     "two cisco inbounds still fight over TCP",
			existing: &model.Inbound{Port: 443, Protocol: model.Cisco, Settings: `{"disableUdp":true}`},
			offered:  &model.Inbound{Port: 443, Protocol: model.Cisco, Settings: `{"disableUdp":true}`},
			clash:    true,
			port:     443,
		},
		{
			name:     "DTLS moved to its own port frees the main one for UDP",
			existing: &model.Inbound{Port: 443, Protocol: model.Cisco, Settings: `{"udpPort":4443}`},
			offered:  &model.Inbound{Port: 443, Protocol: model.Hysteria},
		},
		{
			name:     "and holds the port it moved to",
			existing: &model.Inbound{Port: 443, Protocol: model.Cisco, Settings: `{"udpPort":4443}`},
			offered:  &model.Inbound{Port: 4443, Protocol: model.WireGuard},
			clash:    true,
			port:     4443,
		},
		{
			name:     "but only on UDP",
			existing: &model.Inbound{Port: 443, Protocol: model.Cisco, Settings: `{"udpPort":4443}`},
			offered:  &model.Inbound{Port: 4443, Protocol: model.VLESS, StreamSettings: `{"network":"tcp"}`},
		},
		{
			name:     "a new cisco cannot move DTLS onto a taken UDP port",
			existing: &model.Inbound{Port: 4443, Protocol: model.WireGuard},
			offered:  &model.Inbound{Port: 443, Protocol: model.Cisco, Settings: `{"udpPort":4443}`},
			clash:    true,
			port:     4443,
		},
		{
			name:     "OpenVPN on UDP leaves TCP to a VLESS inbound",
			existing: &model.Inbound{Port: 1194, Protocol: model.OpenVPN, Settings: `{}`},
			offered:  &model.Inbound{Port: 1194, Protocol: model.VLESS, StreamSettings: `{"network":"tcp"}`},
		},
		{
			name:     "OpenVPN on UDP clashes with WireGuard",
			existing: &model.Inbound{Port: 1194, Protocol: model.OpenVPN, Settings: `{}`},
			offered:  &model.Inbound{Port: 1194, Protocol: model.WireGuard},
			clash:    true,
			port:     1194,
		},
		{
			name:     "OpenVPN on TCP clashes with VLESS",
			existing: &model.Inbound{Port: 1194, Protocol: model.OpenVPN, Settings: `{"useTcp":true}`},
			offered:  &model.Inbound{Port: 1194, Protocol: model.VLESS, StreamSettings: `{"network":"tcp"}`},
			clash:    true,
			port:     1194,
		},
		{
			name:     "and leaves UDP to WireGuard",
			existing: &model.Inbound{Port: 1194, Protocol: model.OpenVPN, Settings: `{"useTcp":true}`},
			offered:  &model.Inbound{Port: 1194, Protocol: model.WireGuard},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupConflictDB(t)
			seedInboundConflict(t, "existing", "", tc.existing.Port, tc.existing.Protocol,
				tc.existing.StreamSettings, tc.existing.Settings)
			tc.offered.Tag = "offered"
			got, err := (&InboundService{}).checkPortConflict(tc.offered, 0)
			if err != nil {
				t.Fatalf("checkPortConflict: %v", err)
			}
			switch {
			case tc.clash && got == nil:
				t.Fatal("the two were allowed to share a socket")
			case !tc.clash && got != nil:
				t.Fatalf("refused over a socket nothing holds: %s", got)
			case tc.clash && got.Port != tc.port:
				t.Fatalf("the refusal names port %d, want %d: %s", got.Port, tc.port, got)
			}
		})
	}
}
