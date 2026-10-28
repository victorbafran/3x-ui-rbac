package xray

import (
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// A Psiphon country that a cisco location or an OpenVPN inbound leaves through gets a
// TUN inbound of the exit's own name, which the sidecars' `dev <exit>` route finds.

const psiphonTunTagPrefix = "tun-"

// PsiphonTunTag is the tag of the TUN inbound a Psiphon exit's pools are routed into.
func PsiphonTunTag(exit string) string { return psiphonTunTagPrefix + exit }

// PsiphonTunExit is the exit a generated Psiphon TUN carries, and false for any other
// inbound, a TUN the operator saved among them.
func PsiphonTunExit(ib *InboundConfig) (string, bool) {
	if ib == nil || ib.Protocol != "tun" {
		return "", false
	}
	exit, ok := strings.CutPrefix(ib.Tag, psiphonTunTagPrefix)
	return exit, ok && relay.IsPsiphonExit(exit)
}
