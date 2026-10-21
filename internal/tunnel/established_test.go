package tunnel

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The shape of /proc/net/tcp on a panel dialling a front at
// 185.226.116.154:3080. That address is B9.E2.74.9A on the wire and the
// kernel prints each 32-bit word in host order, so it reads 9A74E2B9; 3080
// is 0C08. The local side, 212.95.35.41, reads 29235FD4.
const procTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 11111 1 0000000000000000 100 0 0 10 0
   1: 29235FD4:B4F2 9A74E2B9:0C08 01 00000000:00000000 02:000A3B9F 00000000     0        0 22222 2 0000000000000000 20 4 30 10 -1
   2: 29235FD4:B4F4 9A74E2B9:0C09 06 00000000:00000000 03:00000F9F 00000000     0        0 0 3 0000000000000000
`

// An IPv4 peer seen from a dual-stack socket: ::ffff:185.226.116.154.
const procTCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0000000000000000FFFF000029235FD4:C001 0000000000000000FFFF00009A74E2B9:0C08 01 00000000:00000000 02:000A3B9F 00000000     0        0 33333 2 0000000000000000 20 4 30 10 -1
`

var frontIP = net.ParseIP("185.226.116.154")

// The connection the panel's client holds to the front is the evidence that
// the tunnel is up. Reading it wrong either way is a status column that lies.
func TestALiveConnectionToTheFrontIsSeen(t *testing.T) {
	if !establishedTo(procTCP, frontIP, 3080) {
		t.Fatal("the established socket to 185.226.116.154:3080 was not found")
	}
}

// A socket on its way out is not a tunnel anybody can ride.
func TestAClosingSocketIsNotALiveTunnel(t *testing.T) {
	if establishedTo(procTCP, frontIP, 3081) {
		t.Fatal("a TIME_WAIT socket was read as a live tunnel")
	}
}

// Only the front's own address and tunnel port count. A connection to a
// neighbour, or to another port on the same machine, says nothing about this
// tunnel.
func TestAnotherHostOrPortDoesNotCount(t *testing.T) {
	if establishedTo(procTCP, net.ParseIP("185.226.116.155"), 3080) {
		t.Fatal("a connection to another host was credited to this front")
	}
	if establishedTo(procTCP, frontIP, 3082) {
		t.Fatal("a port with no socket at all was read as connected")
	}
}

// The panel may hold the connection on a dual-stack socket, where the kernel
// files an IPv4 peer under tcp6 in mapped form. It is the same front.
func TestAMappedAddressInTheIPv6TableCounts(t *testing.T) {
	if !establishedTo(procTCP6, frontIP, 3080) {
		t.Fatal("the mapped IPv4 peer in tcp6 was not recognised as the front")
	}
}

// The udp transport keeps no connection, so there is nothing to find. Saying
// "not connected" there would mark every working udp tunnel as broken.
func TestTheUDPTransportCannotBeReadThisWay(t *testing.T) {
	known, _ := Established(Pair{FrontAddress: "185.226.116.154", TunnelPort: 3080, Transport: TransportUDP})
	if known {
		t.Fatal("a udp tunnel was judged from a TCP table it never appears in")
	}
}

// A front given by name would need a lookup to match against the table. The
// list view does not wait on DNS, so it says it does not know.
func TestAFrontGivenByNameIsNotJudged(t *testing.T) {
	known, _ := Established(Pair{FrontAddress: "tun.example.ir", TunnelPort: 3080, Transport: TransportTCP})
	if known {
		t.Fatal("a front named rather than addressed was judged without resolving it")
	}
}

// End to end through the real reader: the same front is up while its socket
// is in the table and down the moment it is not.
func TestEstablishedReadsTheKernelTable(t *testing.T) {
	dir := t.TempDir()
	withSocket := filepath.Join(dir, "tcp")
	if err := os.WriteFile(withSocket, []byte(procTCP), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "tcp-empty")
	if err := os.WriteFile(empty, []byte("  sl  local_address rem_address   st\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := procTCPTables
	t.Cleanup(func() { procTCPTables = saved })

	p := Pair{FrontAddress: "185.226.116.154", TunnelPort: 3080, Transport: TransportTCP}

	procTCPTables = []string{withSocket}
	if known, up := Established(p); !known || !up {
		t.Fatalf("with the socket present: known=%v up=%v, want both true", known, up)
	}

	procTCPTables = []string{empty}
	if known, up := Established(p); !known || up {
		t.Fatalf("with no socket: known=%v up=%v, want known and down", known, up)
	}

	procTCPTables = []string{filepath.Join(dir, "missing")}
	if known, _ := Established(p); known {
		t.Fatal("a host with no socket table at all was judged anyway")
	}
}

// Garbage in a field is skipped, never misread as an address.
func TestAMalformedFieldIsRejected(t *testing.T) {
	for _, bad := range []string{"", "9A74E2B9", "9A74E2B9:ZZZZ", "XYZ:0C08", "9A74E2:0C08"} {
		if _, _, ok := parseProcAddr(bad); ok {
			t.Errorf("parseProcAddr(%q) accepted a malformed field", bad)
		}
	}
}
