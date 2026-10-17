package ocserv

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
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

	// CertFile/KeyFile point at an operator-supplied TLS keypair. When empty
	// the manager generates and reuses a self-signed RSA keypair.
	//
	// The key must be RSA. An ECDSA key locks out Cisco's own Secure Client:
	// the handshake dies before the certificate is ever sent, so the failure
	// looks like a dead server rather than a certificate problem.
	CertFile string
	KeyFile  string
}

func (inst Instance) listenAddr() string {
	if inst.Listen == "" {
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
		strings.Join(inst.dns(), ","),
		strconv.Itoa(inst.maxClients()),
		strconv.Itoa(inst.maxSameClients()),
		inst.CertFile,
		inst.KeyFile,
	}, "|")
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
	w("listen-host = %s", inst.listenAddr())
	w("tcp-port = %d", inst.Port)
	w("udp-port = %d", inst.Port)
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
	w("device = %s", inst.deviceName())
	w("ipv4-network = %s", inst.network())
	w("ipv4-netmask = %s", inst.netmask())
	for _, d := range inst.dns() {
		w("dns = %s", d)
	}
	w("tunnel-all-dns = true")
	// cisco-client-compat is what makes the real AnyConnect client work; ocserv
	// notes that it implies dtls-legacy, which is wanted for the same reason.
	w("cisco-client-compat = true")
	w("try-mtu-discovery = true")
	w("keepalive = 32400")
	w("dpd = 90")
	w("mobile-dpd = 1800")
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
