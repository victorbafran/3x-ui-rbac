package ocserv

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
	"github.com/mhsanaei/3x-ui/v3/internal/util/filestamp"
)

// Instance is the desired runtime configuration of one cisco inbound. A single
// ocserv process serves every active client of that inbound.
type Instance struct {
	Id     int
	Tag    string
	Listen string
	Port   int
	Users  []User

	// Network/Netmask are the address pool handed to connecting clients. Two
	// instances on one host must not share a pool, so the caller derives a
	// distinct default from the inbound id; see DefaultPool.
	Network string
	Netmask string

	DNS            []string
	MaxClients     int
	MaxSameClients int

	// UDPPort carries DTLS. Zero means the same port as TCP, which is what
	// an AnyConnect client expects; set it only when that UDP port is
	// blocked and another is not.
	UDPPort int

	// ProxyProtocol takes each customer's real address from a PROXY v2 header
	// and listens on loopback only, where nothing but an entry server reaches.
	ProxyProtocol bool

	// DisableUDP turns DTLS off outright. Worth doing when UDP is blocked
	// for good: without it every session spends switch-to-tcp-timeout
	// seconds trying DTLS before falling back, on every single connect.
	DisableUDP bool

	// MTU is the tunnel MTU handed to clients, used when MTUDiscovery is off.
	// Zero takes the panel's default.
	MTU int

	// MTUDiscovery turns path MTU discovery back on for operators whose
	// networks carry ICMP properly and who want the larger packets.
	MTUDiscovery bool

	// MobileDPD is how long ocserv keeps a mobile session it has stopped
	// hearing from before it reaps it, in seconds. Zero takes the default of
	// 1800 — thirty minutes, which is right on a public address where ocserv
	// recognises a returning phone by its IP. Behind a front every client
	// arrives as 127.0.0.1, so a dead session cannot be told from the phone
	// coming back, and thirty minutes of stale sessions pile up and fill the
	// account. A front sets this low.
	MobileDPD int

	// IPv6 answers on this host's IPv6 too, for an inbound handing it out. Off,
	// ocserv listens on IPv4 alone, as every inbound did before the setting.
	IPv6 bool

	// CertFile/KeyFile point at an operator-supplied TLS keypair. When empty
	// the manager generates and reuses a self-signed RSA keypair.
	//
	// The key must be RSA. An ECDSA key locks out Cisco's own Secure Client:
	// the handshake dies before the certificate is ever sent, so the failure
	// looks like a dead server rather than a certificate problem.
	CertFile string
	KeyFile  string

	// Locations are the countries this inbound offers. With any present the
	// client shows a Group dropdown before asking for a password and the
	// customer picks where to come out; each location hands out a pool of
	// its own so the host can route it to that country's exit.
	//
	// Measured on ocserv 1.2.4: once select-group is configured nobody
	// connects without choosing one — not even a client whose password-file
	// group field is "*", which means no group rather than any group. A
	// client entitled to nothing could therefore not connect at all, which
	// is why InstanceFromInbound entitles such a client to every location.
	Locations []Location
}

// hostHasIPv6 is a variable so a test can be the host without IPv6.
var hostHasIPv6 = sidecar.HostHasIPv6

// listenAddr is "" for every address of both families, which ocserv takes
// from no listen-host at all; 0.0.0.0 would leave an IPv6 customer no way in.
func (inst Instance) listenAddr() string {
	// Loopback, or anyone could dial in direct with a forged header.
	if inst.ProxyProtocol {
		return "127.0.0.1"
	}
	if inst.Listen == "" {
		if inst.IPv6 && hostHasIPv6() {
			return ""
		}
		return "0.0.0.0"
	}
	return inst.Listen
}

func (inst Instance) network() string {
	if inst.Network == "" {
		return DefaultPool(inst.Id)
	}
	return inst.Network
}

// ipv6Network is a private /64 per inbound: an address from it pulls a client's
// IPv6 into the tunnel, and this host does not forward IPv6, so none leaks past.
func (inst Instance) ipv6Network() string {
	return fmt.Sprintf("fd5a:3c71:9e04:%x::/64", uint16(inst.Id))
}

func (inst Instance) netmask() string {
	if inst.Netmask == "" {
		return "255.255.255.0"
	}
	return inst.Netmask
}

func (inst Instance) dns() []string {
	if len(inst.DNS) == 0 {
		return []string{"1.1.1.1", "8.8.8.8"}
	}
	return inst.DNS
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

func (inst Instance) udpPort() int {
	if inst.UDPPort <= 0 {
		return inst.Port
	}
	return inst.UDPPort
}

// 1400 leaves room for the DTLS record, the UDP and IP headers and a layer of
// encapsulation on the way — the common case on paths that are not plain
// ethernet end to end.
func (inst Instance) mtu() int {
	if inst.MTU <= 0 {
		return 1400
	}
	return inst.MTU
}

func (inst Instance) mobileDPD() int {
	if inst.MobileDPD <= 0 {
		return 1800
	}
	return inst.MobileDPD
}

// deviceName is the tun interface prefix; ocserv appends a per-session number.
func (inst Instance) deviceName() string { return fmt.Sprintf("vpns%d", inst.Id) }

// DefaultPool derives a /24 that will not collide with another cisco inbound on
// the same host. Two instances handing out the same addresses would route each
// other's clients, so the pool is a function of the inbound id rather than a
// shared constant.
func DefaultPool(id int) string {
	// 10.D.S.0/24 walks 10.8.0.0 upward: 240 second octets × 256 third octets
	// is far more room than a host will ever have inbounds.
	n := id
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("10.%d.%d.0", 8+(n/256)%240, n%256)
}

// CIDR renders the instance pool as a prefix, for the NAT rule and for display.
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

// structuralFingerprint changes whenever something that ocserv only reads at
// startup changes. Such a change forces a restart, which drops every live
// session, so it deliberately excludes the user list: ocserv consults the
// password file on each authentication, making client edits free.
func (inst Instance) structuralFingerprint() string {
	return strings.Join([]string{
		inst.listenAddr(),
		strconv.Itoa(inst.Port),
		inst.network(),
		inst.netmask(),
		inst.ipv6Network(),
		strings.Join(inst.dns(), ","),
		strconv.Itoa(inst.maxClients()),
		strconv.Itoa(inst.maxSameClients()),
		inst.CertFile,
		inst.KeyFile,
		// The content as well as the path. ocserv reads its certificate once
		// at startup, and a certificate renewed in place kept being served
		// stale until something else happened to restart it.
		filestamp.Of(inst.CertFile),
		filestamp.Of(inst.KeyFile),
		inst.generatedPairStamp(),
		strconv.Itoa(inst.udpPort()),
		strconv.FormatBool(inst.DisableUDP),
		strconv.FormatBool(inst.ProxyProtocol),
		strconv.Itoa(inst.mtu()),
		strconv.FormatBool(inst.MTUDiscovery),
		strconv.Itoa(inst.mobileDPD()),
		inst.locationsFingerprint(),
	}, "|")
}

// generatedPairStamp covers the pair made for an inbound without its own,
// which a restore can replace under a running ocserv.
func (inst Instance) generatedPairStamp() string {
	if inst.CertFile != "" && inst.KeyFile != "" {
		return ""
	}
	return filestamp.Of(certPathForID(inst.Id)) + "," + filestamp.Of(keyPathForID(inst.Id))
}

// locationsFingerprint covers the country list in the order it is rendered,
// because a location's pool is derived from its position: moving two countries
// past each other swaps their addresses, which ocserv only reads at startup.
func (inst Instance) locationsFingerprint() string {
	locs, _ := inst.locations()
	if len(locs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(locs))
	for i, l := range locs {
		parts = append(parts, strings.Join([]string{
			l.Name, l.labelOrName(), l.CIDR(inst.Id, i),
			strings.Join(l.DNS, ","), strings.TrimSpace(l.Exit),
		}, ">"))
	}
	return strings.Join(parts, ";")
}

// usersFingerprint identifies the client set regardless of order, so a
// reordered clients array in the stored settings does not read as a change.
// It is computed over the desired plaintext rather than the rendered file
// because each render salts its hashes afresh: comparing rendered output would
// report a change on every single reconcile.
func (inst Instance) usersFingerprint() string {
	pairs := make([]string, 0, len(inst.Users))
	for _, u := range inst.Users {
		pairs = append(pairs, fmt.Sprintf("%s=%s;dev=%d", u.Login, u.Password, u.DeviceLimit))
	}
	slices.Sort(pairs)
	return strings.Join(pairs, "|")
}

// UserNames lists the client emails this instance should serve, sorted. These
// are the panel identities traffic is billed against, not the login names.
func (inst Instance) UserNames() []string {
	names := make([]string, 0, len(inst.Users))
	for _, u := range inst.Users {
		names = append(names, u.Name)
	}
	slices.Sort(names)
	return names
}

// LoginToEmail maps what a client types at login back to the panel identity
// traffic is billed against. occtl only ever reports the login name, so
// without this map a reseller customer would accrue traffic under a name no
// client record has.
func (inst Instance) LoginToEmail() map[string]string {
	m := make(map[string]string, len(inst.Users))
	for _, u := range inst.Users {
		m[u.Login] = u.Name
	}
	return m
}

// renderConfig writes the whole ocserv.conf for one instance. Every value is
// stated explicitly rather than inherited from the distribution's config file,
// so what the panel shows is what the server runs.
func renderConfig(inst Instance, certFile, keyFile string) string {
	var b strings.Builder
	w := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	w("# generated by x-ui for inbound %d (%s) — edits are overwritten", inst.Id, inst.Tag)
	w("auth = \"plain[passwd=%s]\"", passwdPathForID(inst.Id))
	if addr := inst.listenAddr(); addr != "" {
		w("listen-host = %s", addr)
	}
	w("tcp-port = %d", inst.Port)
	if inst.ProxyProtocol {
		w("listen-proxy-proto = true")
	}
	// Without a udp-port there is no DTLS at all, and the client stops
	// waiting for one. With UDP blocked that is the faster answer: DTLS is
	// what makes this protocol quick, but a DTLS that never forms only
	// costs every connection the switch-to-tcp timeout.
	if !inst.DisableUDP {
		w("udp-port = %d", inst.udpPort())
	}
	w("run-as-user = nobody")
	w("run-as-group = nogroup")
	w("socket-file = %s", ipcSocketForID(inst.Id))
	w("use-occtl = true")
	w("occtl-socket-file = %s", ctlSocketForID(inst.Id))
	w("server-cert = %s", certFile)
	w("server-key = %s", keyFile)
	w("max-clients = %d", inst.maxClients())
	w("max-same-clients = %d", inst.maxSameClients())
	// Per-client overrides live here, one file per login. A client with no
	// file of its own falls through to the max-same-clients above.
	w("config-per-user = %s/", perUserDirForID(inst.Id))
	// One file per country, holding that country's pool and resolvers, and
	// one select-group line per country, which is what puts the names in the
	// customer's dropdown. Written only when there are locations: a bare
	// select-group list with nothing behind it would force every client to
	// choose from an empty menu.
	if locs, _ := inst.locations(); len(locs) > 0 {
		w("config-per-group = %s/", perGroupDirForID(inst.Id))
		for _, l := range locs {
			w("select-group = %s[%s]", l.Name, l.labelOrName())
		}
	}
	w("device = %s", inst.deviceName())
	w("ipv4-network = %s", inst.network())
	w("ipv4-netmask = %s", inst.netmask())
	w("ipv6-network = %s", inst.ipv6Network())
	w("ipv6-subnet-prefix = 128")
	for _, d := range inst.dns() {
		w("dns = %s", d)
	}
	w("tunnel-all-dns = true")
	// cisco-client-compat is what makes the real AnyConnect client work; ocserv
	// notes that it implies dtls-legacy, which is wanted for the same reason.
	w("cisco-client-compat = true")
	// output-buffer is deliberately never written. The manual reads as though
	// raising it buys throughput, so this panel used to set 64 — and measured
	// against a stock ocserv on the same server, over the same path, in the same
	// minute, that one line cost 223 Mbit/s of download and turned a clean
	// 50 Mbit/s UDP stream into 87% loss. Every value behaved the same: 4, 23
	// (ocserv's own default), 32, 64 and 1000 all landed within a megabit of
	// each other around 5 Mbit/s, while omitting the option restored full speed.
	// The size is not what matters; naming the option at all is.
	// Path MTU discovery is off by default in ocserv's shipped config, and
	// for good reason on the networks this panel serves: it depends on ICMP
	// getting back, and a path that swallows ICMP leaves the tunnel guessing
	// high and then stalling on every large packet. A fixed, conservative
	// MTU is slower in theory and far steadier in practice.
	if inst.MTUDiscovery {
		w("try-mtu-discovery = true")
	} else {
		w("try-mtu-discovery = false")
		w("mtu = %d", inst.mtu())
	}
	w("keepalive = 32400")
	w("dpd = 90")
	w("mobile-dpd = %d", inst.mobileDPD())
	w("switch-to-tcp-timeout = 25")
	w("auth-timeout = 240")
	w("idle-timeout = 1200")
	w("mobile-idle-timeout = 2400")
	w("cookie-timeout = 300")
	w("rekey-time = 172800")
	w("rekey-method = ssl")
	w("ping-leases = false")
	w("predictable-ips = true")
	return b.String()
}
