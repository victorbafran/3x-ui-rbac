package openvpn

import (
	"fmt"
	"os"
	"strings"
)

// The .ovpn file is the whole of what a customer gets. Everything it needs is
// embedded in it — the authority, the shared key — because a profile that
// refers to files beside it is one a customer has to install rather than
// open, and most of them will not manage it.

// Profile describes one file for one customer.
type Profile struct {
	// Address is what the customer's client connects to: a hostname when the
	// operator has one, the server's address otherwise.
	Address string
	// Address6 is a front's IPv6, written as a remote ahead of Address so the
	// client tries it first and falls back; "" when there is none.
	Address6 string
	// Label names the file for them, which is what their client shows in its
	// list of connections.
	Label string
}

// Render writes the profile for an instance.
//
// The client half of the leak protection lives here as well as in the pushed
// options. Pushing is a request the client may ignore; these are in the
// customer's own file and apply even to a client that ignores what is pushed.
func Render(inst Instance, p Profile) (string, error) {
	ca, err := os.ReadFile(caCertPathForID(inst.Id))
	if err != nil {
		return "", fmt.Errorf("the certificate authority for inbound %d is missing; the inbound has not started yet: %w", inst.Id, err)
	}
	key, err := os.ReadFile(tlsAuthPathForID(inst.Id))
	if err != nil {
		return "", fmt.Errorf("the shared key for inbound %d is missing; the inbound has not started yet: %w", inst.Id, err)
	}

	address := remoteHost(p.Address)
	if address == "" {
		return "", fmt.Errorf("no address for the customer to connect to")
	}

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	if label := strings.TrimSpace(p.Label); label != "" {
		w("# %s", label)
	}
	w("client")
	w("dev tun")
	w("proto %s", clientProto(inst))
	address6 := remoteHost(p.Address6)
	fallsBack := address6 != "" && address6 != address
	if fallsBack {
		w("remote %s %d", address6, inst.Port)
	}
	w("remote %s %d", address, inst.Port)
	if fallsBack {
		// A dead IPv6 path must not cost two minutes: OpenVPN 2 and 3 read the
		// first line, OpenVPN for Android only the second (an alias in OpenVPN 2.4+).
		w("server-poll-timeout 10")
		w("connect-timeout 10")
	}
	// A name that moves, or a server that is restarting, should be waited for
	// rather than treated as a dead profile.
	w("resolv-retry infinite")
	w("nobind")
	w("persist-key")
	w("persist-tun")

	// The customer types a name and a password. Without this line the client
	// has no idea it should ask for them and fails with a handshake error
	// that says nothing about credentials.
	w("auth-user-pass")
	// There is no client certificate in this profile, and a client that is not
	// told so stops to ask the customer to pick one out of their phone's
	// keychain — a question they have no way to answer. This is the line the
	// OpenVPN clients read to mean "username and password only".
	w("setenv CLIENT_CERT 0")
	// The server's certificate must carry the server extended-key-usage, so a
	// certificate issued for something else cannot stand in for it. This is
	// the whole of what the client checks: there is deliberately no
	// verify-x509-name, which pins a subject name this panel never fixes.
	w("remote-cert-tls server")

	w("data-ciphers AES-256-GCM:AES-128-GCM")
	w("data-ciphers-fallback AES-256-GCM")
	w("auth SHA256")
	w("tls-version-min 1.2")

	// The server pushes these too. They are repeated here because a push is a
	// request: a client that ignores it would send every name it looks up to
	// the customer's own provider, and every IPv6 packet straight out of
	// their own connection with their real address on it.
	w("block-ipv6")
	w("setenv opt block-outside-dns")
	w("verb 3")

	w("")
	w("<ca>")
	b.WriteString(ensureTrailingNewline(string(ca)))
	w("</ca>")
	w("<tls-crypt>")
	b.WriteString(ensureTrailingNewline(string(key)))
	w("</tls-crypt>")
	return b.String(), nil
}

// remoteHost is an address as a remote line takes it: OpenVPN reads an IPv6
// literal bare, and "[2001:db8::1]" is a host name it fails to resolve.
func remoteHost(addr string) string {
	return strings.Trim(strings.TrimSpace(addr), "[]")
}

// clientProto is the client's spelling. The server says tcp-server; a client
// saying the same thing refuses to start.
func clientProto(inst Instance) string {
	if inst.UseTCP {
		return "tcp-client"
	}
	return "udp"
}

func ensureTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

// FileName is what the customer's browser saves it as. The country is in the
// name because a customer with several has several files, and "client.ovpn"
// three times over tells them nothing about which is which.
func FileName(label string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r == ' ':
			return '-'
		default:
			return -1
		}
	}, label)
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		cleaned = "vpn"
	}
	return cleaned + ".ovpn"
}
