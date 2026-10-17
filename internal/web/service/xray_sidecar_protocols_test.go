package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Handing xray-core an inbound whose protocol it has no handler for is not a
// partial failure. The core refuses the entire config file and exits, so one
// unknown inbound takes down every other inbound on the panel. Measured on a
// live panel: a cisco inbound in the config left xray in a restart loop with
// "unknown config id: cisco" while every vless client stayed offline.
//
// So each sidecar protocol must be absent from the generated config, and this
// is asserted per protocol rather than for cisco alone, because the way this
// breaks is someone adding the next sidecar and updating only some of the
// places that used to spell the set out by hand.
func TestGeneratedXrayConfigExcludesEverySidecarProtocol(t *testing.T) {
	sidecars := []model.Protocol{model.MTProto, model.AmneziaWG, model.TUIC, model.Cisco}

	setupConflictDB(t)
	seedInboundConflict(t, "in-51820-vless", "0.0.0.0", 51820, model.VLESS, "{}", `{"clients":[]}`)
	for i, protocol := range sidecars {
		seedInboundConflict(t, "sidecar-"+string(protocol), "0.0.0.0", 52000+i, protocol, "{}",
			`{"clients":[{"email":"c@example.com","password":"p","secret":"s","enable":true}]}`)
	}

	svc := &XrayService{}
	cfg, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}

	// The assertion is on the emitted protocol, not on the tag. A sidecar may
	// legitimately own an entry in the config — TUIC gets a loopback socks
	// relay carrying the same tag, so its egress obeys xray's routing — and
	// that is fine precisely because its protocol is one xray can build.
	emittedProtocols := make(map[string]string, len(cfg.InboundConfigs))
	emittedTags := make(map[string]bool, len(cfg.InboundConfigs))
	for i := range cfg.InboundConfigs {
		ic := cfg.InboundConfigs[i]
		emittedProtocols[ic.Protocol] = ic.Tag
		emittedTags[ic.Tag] = true
	}
	for _, protocol := range sidecars {
		if tag, found := emittedProtocols[string(protocol)]; found {
			t.Errorf("inbound %q was emitted with protocol %q, which xray-core has no handler for; the core refuses the whole config file and every other inbound goes down with it", tag, protocol)
		}
	}
	// The control: an ordinary inbound still has to be there, or this test
	// would also pass against a config builder that emitted nothing at all.
	if !emittedTags["in-51820-vless"] {
		t.Fatal("the ordinary vless inbound is missing from the config too; the filter is excluding more than the sidecars")
	}
}

// The one list every caller reads. A protocol xray does serve must never land
// in it: it would silently stop being served at all.
func TestServedOutsideXrayNamesExactlyTheSidecars(t *testing.T) {
	outside := map[model.Protocol]bool{
		model.MTProto: true, model.AmneziaWG: true, model.TUIC: true, model.Cisco: true,
	}
	inside := []model.Protocol{
		model.VMESS, model.VLESS, model.Trojan, model.Shadowsocks,
		model.WireGuard, model.Hysteria, model.HTTP, model.Mixed, model.Tunnel, model.MASQUE,
	}
	for p := range outside {
		if !p.ServedOutsideXray() {
			t.Errorf("%s has its own sidecar but is handed to xray-core", p)
		}
	}
	for _, p := range inside {
		if p.ServedOutsideXray() {
			t.Errorf("%s is served by xray-core but was excluded from its config", p)
		}
	}
}
