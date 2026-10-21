package tunnel

import (
	"bufio"
	"encoding/hex"
	"net"
	"os"
	"strconv"
	"strings"
)

// procTCPTables are the kernel's socket tables. The panel dials an IPv4 front
// from an IPv4 socket, which lands in the first; the second is read as well
// because a dual-stack socket reports an IPv4 peer there in mapped form.
var procTCPTables = []string{"/proc/net/tcp", "/proc/net/tcp6"}

// Established reports whether the panel holds a live TCP connection to the
// front's tunnel port.
//
// A running client is not a connected one. Backhaul retries for ever against
// a front that has gone away, so the process alone would show a dead tunnel
// as working, which is the one answer a status column must never give.
//
// known is false when this cannot be read off the host: the udp transport
// holds no connection to look for, and a front given by name rather than by
// address cannot be matched against the table without a lookup that the
// list view should not wait on. The caller then has only the process.
func Established(p Pair) (known, up bool) {
	if !carriedOverTCP(p.Transport) {
		return false, false
	}
	ip := net.ParseIP(strings.TrimSpace(p.FrontAddress))
	if ip == nil {
		return false, false
	}
	for _, path := range procTCPTables {
		table, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		known = true
		if establishedTo(string(table), ip, p.TunnelPort) {
			return true, true
		}
	}
	return known, false
}

// carriedOverTCP is whether a transport keeps TCP connections open to the
// front. udp, kcp and quic ride datagrams and leave nothing in the TCP table.
func carriedOverTCP(t Transport) bool {
	return tcpCarried(t)
}

// establishedTo reads a socket table in the format of /proc/net/tcp or
// /proc/net/tcp6 for an ESTABLISHED socket whose far end is ip:port. A
// socket in any other state, a closing one included, is not a live tunnel.
func establishedTo(table string, ip net.IP, port int) bool {
	sc := bufio.NewScanner(strings.NewReader(table))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// sl, local, remote, state. The header line has "st" where the
		// state goes, so it never matches and needs no special case.
		if len(f) < 4 || f[3] != "01" {
			continue
		}
		rip, rport, ok := parseProcAddr(f[2])
		if ok && rport == port && rip.Equal(ip) {
			return true
		}
	}
	return false
}

// parseProcAddr decodes one address:port field of the socket table. The
// address is hex in host byte order one 32-bit word at a time, so on the
// little-endian machines a panel runs on every group of four bytes reads
// backwards. The port is plain hex.
func parseProcAddr(s string) (net.IP, int, bool) {
	host, portHex, found := strings.Cut(s, ":")
	if !found {
		return nil, 0, false
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return nil, 0, false
	}
	raw, err := hex.DecodeString(host)
	if err != nil || (len(raw) != net.IPv4len && len(raw) != net.IPv6len) {
		return nil, 0, false
	}
	ip := make(net.IP, len(raw))
	for w := 0; w < len(raw); w += 4 {
		ip[w], ip[w+1], ip[w+2], ip[w+3] = raw[w+3], raw[w+2], raw[w+1], raw[w]
	}
	return ip, int(port), true
}
