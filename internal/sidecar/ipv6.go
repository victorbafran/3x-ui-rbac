package sidecar

import (
	"net"
	"sync"
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
