package sidecar

import (
	"encoding/json"
	"net"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// HostHasIPv6 is whether this host can open an IPv6 socket at all. A kernel
// booted with ipv6.disable=1 cannot, and a server told to listen on [::] exits.
var HostHasIPv6 = sync.OnceValue(func() bool {
	conn, err := net.ListenPacket("udp6", "[::]:0")
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
})

// IPv6Settings is what a cisco or OpenVPN inbound's settings say about IPv6
// served by this server itself, named as a front's are.
type IPv6Settings struct {
	// IPFamily is what customers are given: "" IPv4 as before, "v6", or "both".
	IPFamily string `json:"ipFamily"`
	// Address6 is what their IPv6 dials: a name, or an IPv6 literal.
	Address6 string `json:"address6"`
}

// IPv6SettingsOf reads them from an inbound's settings JSON, trimmed; an
// unreadable document is the IPv4 every inbound had before.
func IPv6SettingsOf(settings string) IPv6Settings {
	var s IPv6Settings
	if json.Unmarshal([]byte(settings), &s) != nil {
		return IPv6Settings{}
	}
	return IPv6Settings{IPFamily: strings.TrimSpace(s.IPFamily), Address6: strings.TrimSpace(s.Address6)}
}

// ServesIPv6 is whether the inbound's server has to answer on IPv6 too.
func (s IPv6Settings) ServesIPv6() bool {
	return s.IPFamily == relay.IPFamilyV6 || s.IPFamily == relay.IPFamilyBoth
}
