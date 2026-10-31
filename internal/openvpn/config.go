package openvpn

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
	"github.com/mhsanaei/3x-ui/v3/internal/util/filestamp"
)

// Instance is one OpenVPN sidecar: everything the panel knows about an
// inbound, in the shape the process needs it.
type Instance struct {
	Id     int
	Tag    string
	Listen string
	Port   int
	Users  []User

	// Network and Netmask are the pool handed to clients. Empty takes
	// DefaultPool, which keeps two inbounds on one host from handing out the
	// same addresses and routing each other's customers.
	Network string
	Netmask string

	DNS        []string
	MaxClients int

	// MaxSameClients caps how many devices one account may hold at once.
	MaxSameClients int

	// UseTCP serves the inbound over TCP instead of UDP. UDP is faster and is
	// the default; TCP is for a network that drops UDP outright.
	UseTCP bool

	// Exit is the interface this inbound's traffic leaves through — a tunnel
	// to a server in another country. Empty leaves through this host.
	Exit string
}

// User is one client. Login is what the customer types; Name is the panel's
// identity for them, which is what traffic is billed against.
type User struct {
	Name     string
	Login    string
	Password string
	// DeviceLimit caps this one account. Zero leaves it on the inbound's.
	DeviceLimit int
}

// hostHasIPv6 is a variable so a test can be the host without IPv6.
var hostHasIPv6 = sidecar.HostHasIPv6

// dualStack serves both families from one socket on [::] (OpenVPN 2.6.19,
// measured: udp6 takes IPv4 too). An explicit listen keeps the family it names.
func (inst Instance) dualStack() bool {
	return strings.TrimSpace(inst.Listen) == "" && hostHasIPv6()
}

// listenAddr is "" for a dual-stack server: a "local" line would pin one family.
func (inst Instance) listenAddr() string {
	if inst.dualStack() {
		return ""
	}
	if strings.TrimSpace(inst.Listen) == "" {
		return "0.0.0.0"
	}
	return inst.Listen
}

func (inst Instance) proto() string {
	switch {
	case inst.UseTCP && inst.dualStack():
		return "tcp6-server"
	case inst.UseTCP:
		return "tcp-server"
	case inst.dualStack():
		return "udp6"
	}
	return "udp"
}

func (inst Instance) network() string {
	if n := strings.TrimSpace(inst.Network); n != "" {
		return n
	}
	return DefaultPool(inst.Id)
}

func (inst Instance) netmask() string {
	if m := strings.TrimSpace(inst.Netmask); m != "" {
		return m
	}
	return "255.255.255.0"
}

func (inst Instance) dns() []string {
	out := make([]string, 0, len(inst.DNS))
	for _, d := range inst.DNS {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return []string{"1.1.1.1", "8.8.8.8"}
	}
	return out
}

func (inst Instance) maxClients() int {
	if inst.MaxClients <= 0 {
		return 1024
	}
	return inst.MaxClients
}

func (inst Instance) maxSameClients() int {
	if inst.MaxSameClients <= 0 {
		return 2
	}
	return inst.MaxSameClients
}

// deviceName is the tun interface. Named after the inbound so two of them
// cannot collide, and short enough for the 15 characters linux allows.
func (inst Instance) deviceName() string { return fmt.Sprintf("ovpn%d", inst.Id) }

// DefaultPool derives a /24 that will not collide with another OpenVPN
// inbound, with a cisco inbound, or with the block exit-server locations use.
//
// cisco's DefaultPool walks 10.8-10.247 and locations take 10.248-10.255, so
// OpenVPN takes 10.1-10.7: eight second octets, 256 third octets, more room
// than a host will ever have inbounds.
func DefaultPool(id int) string {
	n := id
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("10.%d.%d.0", 1+(n/256)%7, n%256)
}

// CIDR renders the pool as a prefix, for the NAT and routing rules.
func (inst Instance) CIDR() string {
	return inst.network() + "/" + strconv.Itoa(maskBits(inst.netmask()))
}

func maskBits(mask string) int {
	bits := 0
	for _, part := range strings.Split(mask, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 24
		}
		for ; n > 0; n >>= 1 {
			bits += n & 1
		}
	}
	if bits == 0 {
		return 24
	}
	return bits
}

// structuralFingerprint changes whenever something the process only reads at
// startup changes. Such a change restarts the sidecar, which drops every live
// session, so it deliberately excludes the user list: credentials are checked
// per connection by a helper that reads the file each time.
func (inst Instance) structuralFingerprint() string {
	return strings.Join([]string{
		inst.listenAddr(),
		strconv.Itoa(inst.Port),
		inst.proto(),
		inst.network(),
		inst.netmask(),
		strings.Join(inst.dns(), ","),
		strconv.Itoa(inst.maxClients()),
		strconv.Itoa(inst.maxSameClients()),
		inst.ownKeysStamp(),
		strings.TrimSpace(inst.Exit),
	}, "|")
}

// ownKeysStamp covers the keys the panel made for this inbound, which a
// restore can replace under a running server.
func (inst Instance) ownKeysStamp() string {
	paths := []string{tlsAuthPathForID(inst.Id), caCertPathForID(inst.Id), certPathForID(inst.Id), keyPathForID(inst.Id)}
	stamps := make([]string, len(paths))
	for i, p := range paths {
		stamps[i] = filestamp.Of(p)
	}
	return strings.Join(stamps, ",")
}

// renderConfig writes the whole server config for one instance.
//
// Two of these lines are here because of what the cisco work taught, and both
// are about things the manuals get wrong:
//
//   - sndbuf and rcvbuf are never written. Guides everywhere tell you to set
//     them to a few hundred kilobytes, and OpenVPN's own documentation now
//     says to leave them alone. Setting them pins the socket buffer and turns
//     off the kernel's own sizing — which is exactly what ocserv's
//     output-buffer did, and that one line cost 223 Mbit/s of download.
//   - no compression of any kind. It is deprecated, it is a known weakness
//     (VORACLE), and on a fast link it costs more than it saves.
//
// Nothing here is tuned on faith. The MTU is left at OpenVPN's own default
// until a measurement says otherwise, for the same reason.
// HelperCommand is what openvpn is told to run to check a password: the
// panel's own binary, the subcommand, and which inbound is asking.
//
// It is a function rather than a string built at the call site because the
// test used to build its own — a bare path, no arguments — and so never saw
// that the real one is three words long and has to be quoted. The config that
// shipped was rejected by openvpn at startup, and no test could have caught
// it while the test fed the renderer a value the caller never produces.
func HelperCommand(binary string, inboundID int) string {
	return fmt.Sprintf("%s openvpn-verify %d", binary, inboundID)
}

func renderConfig(inst Instance, helperPath string, drop *DropAccount) string {
	var b strings.Builder
	w := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	w("# generated by x-ui for inbound %d (%s) — edits are overwritten", inst.Id, inst.Tag)
	if addr := inst.listenAddr(); addr != "" {
		w("local %s", addr)
	}
	w("port %d", inst.Port)
	w("proto %s", inst.proto())
	if !inst.UseTCP {
		// A reply from another local address is rejected by the client; TCP
		// already answers from the connection. Harmless on a single bound address.
		w("multihome")
	}
	w("dev-type tun")
	w("dev %s", inst.deviceName())
	// topology subnet gives every client one address out of the pool rather
	// than a /30 each, which is what any client made this decade expects.
	w("topology subnet")
	w("server %s %s", inst.network(), inst.netmask())

	w("ca %s", caCertPathForID(inst.Id))
	w("cert %s", certPathForID(inst.Id))
	w("key %s", keyPathForID(inst.Id))
	// No Diffie-Hellman parameter file: the key exchange is ECDHE, which
	// needs none and is faster. Generating a dh.pem would delay a first start
	// by minutes for nothing.
	w("dh none")
	// Every packet carries an HMAC made with this key, and one that does not
	// is dropped before the TLS stack ever sees it. It keeps a scanner from
	// learning there is an OpenVPN here, and keeps anybody without the key
	// away from the handshake entirely.
	w("tls-crypt %s", tlsAuthPathForID(inst.Id))
	w("tls-version-min 1.2")

	// The customer types a name and a password; there is no per-client
	// certificate to issue, revoke or lose. The helper is the panel's own
	// binary, which checks the same bcrypt file the cisco side uses, so a
	// customer who has both protocols has one password for both.
	w("verify-client-cert none")
	w("username-as-common-name")
	w("script-security 2")
	// The command and its arguments are ONE parameter, so they are quoted.
	// openvpn counts words: unquoted, "<binary> openvpn-verify 3 via-file" is
	// four parameters where at most two are allowed, and the server exits at
	// startup with an options error. The inbound is then simply absent, which
	// is the hardest kind of failure for an operator to read.
	w("auth-user-pass-verify %q via-file", helperPath)
	// Without this the helper is asked again on every renegotiation, which
	// means a customer whose password changed is thrown off mid-session.
	w("reneg-sec 0")

	w("data-ciphers AES-256-GCM:AES-128-GCM")
	w("data-ciphers-fallback AES-256-GCM")
	w("auth SHA256")

	w("keepalive 10 60")
	w("persist-key")
	w("persist-tun")
	// Only when there is an account to drop to AND that account can read
	// the credentials file. ResolveDropAccount decides both at once; see
	// drop.go for why they must not be decided separately.
	if drop != nil {
		w("user %s", drop.User)
		w("group %s", drop.Group)
	}
	w("max-clients %d", inst.maxClients())
	// Several devices on one account are the normal case, and the per-client
	// limit is enforced through the management interface rather than here:
	// this switch is all or nothing, and the operator sets a number.
	w("duplicate-cn")

	// What the client is told to do with its own traffic. Each of these three
	// closes a way the customer's real address or browsing leaks out around
	// the tunnel, and none of them is on by default.
	w("push \"redirect-gateway def1 bypass-dhcp\"")
	for _, d := range inst.dns() {
		w("push \"dhcp-option DNS %s\"", d)
	}
	// Windows will happily keep using the resolver it had, so every name the
	// customer looks up reaches their own provider even though the traffic
	// does not.
	w("push \"block-outside-dns\"")
	// This host serves IPv4 only. A client with IPv6 would send that traffic
	// straight out of its own connection, with its real address on it.
	w("push \"block-ipv6\"")

	// Read by the traffic job. Version 2 is the parseable one; version 1 is
	// meant for a human and changes shape between releases.
	w("status %s 10", statusPathForID(inst.Id))
	w("status-version 2")
	// A unix socket, not a localhost port: a port is reachable by every
	// process on the host, and this interface can disconnect any customer.
	w("management %s unix", managementSocketForID(inst.Id))

	if !inst.UseTCP {
		// Tells the client the server is going away, so it reconnects at once
		// instead of waiting for the keepalive to time out.
		w("explicit-exit-notify 1")
	}
	w("verb 3")
	w("mute 20")
	return b.String()
}

// LoginToEmail maps what the server reports back to the panel identity that
// traffic is billed against. The two differ for every reseller client, whose
// email carries a namespace a login name cannot hold.
func (inst Instance) LoginToEmail() map[string]string {
	out := make(map[string]string, len(inst.Users))
	for _, u := range inst.Users {
		out[u.Login] = u.Name
	}
	return out
}

// Limits is the device cap per login: the client's own when it has one, the
// inbound's otherwise. The operator sets a number and expects that number,
// which is the whole point of having it per client.
func (inst Instance) Limits() map[string]int {
	out := make(map[string]int, len(inst.Users))
	for _, u := range inst.Users {
		if u.DeviceLimit > 0 {
			out[u.Login] = u.DeviceLimit
			continue
		}
		out[u.Login] = inst.maxSameClients()
	}
	return out
}

// usersFingerprint identifies the client set regardless of order, so a
// reordered clients array in the stored settings does not read as a change.
// It is computed over the wanted plaintext rather than the rendered file:
// each render salts its hashes afresh, so comparing output would report a
// change on every single reconcile and rewrite the file forever.
func usersFingerprint(users []User) string {
	parts := make([]string, 0, len(users))
	for _, u := range users {
		parts = append(parts, u.Login+":"+u.Password+":"+strconv.Itoa(u.DeviceLimit))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}
