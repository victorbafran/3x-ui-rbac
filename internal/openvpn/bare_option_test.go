package openvpn

import (
	"strings"
	"testing"
)

// needsAnArgument lists directives OpenVPN refuses outright when they are
// written bare. There is no warning and no degraded mode: the client stops
// with ERR_INVALID_OPTION_VAL and the customer sees "Connection Failed", so a
// profile carrying one of these is not a worse profile — it is one nobody can
// open.
//
// verify-x509-name heads the list because that is exactly what shipped. It
// went in with no name at all, under a comment claiming it said something
// about the client's own certificate, which it does not. Every customer who
// downloaded a profile got a file their app rejected.
//
// Only options that genuinely require an argument belong here. Several
// neighbours in these configs — auth-user-pass, redirect-gateway,
// explicit-exit-notify — are legal bare and would turn this into a test that
// fails on correct output.
var needsAnArgument = map[string]bool{
	"auth":                  true,
	"auth-user-pass-verify": true,
	"ca":                    true,
	"cert":                  true,
	"cipher":                true,
	"data-ciphers":          true,
	"data-ciphers-fallback": true,
	"dev":                   true,
	"dhcp-option":           true,
	"ifconfig":              true,
	"keepalive":             true,
	"key":                   true,
	"management":            true,
	"max-clients":           true,
	"port":                  true,
	"proto":                 true,
	"push":                  true,
	"remote":                true,
	"remote-cert-tls":       true,
	"reneg-sec":             true,
	"resolv-retry":          true,
	"route":                 true,
	"script-security":       true,
	"server":                true,
	"setenv":                true,
	"status":                true,
	"tls-auth":              true,
	"tls-crypt":             true,
	"tls-version-min":       true,
	"topology":              true,
	"verb":                  true,
	"verify-x509-name":      true,
}

// findBareOptions returns every line that names one of those directives and
// gives it nothing.
func findBareOptions(text string) []string {
	var bare []string
	inInlineBlock := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		// The certificate and key blocks are base64 and PEM, not directives.
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
			inInlineBlock = !strings.HasPrefix(line, "</")
			continue
		}
		if inInlineBlock || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if needsAnArgument[fields[0]] && len(fields) == 1 {
			bare = append(bare, line)
		}
	}
	return bare
}

// The customer's file is the one that broke, so it is checked first.
func TestTheCustomersProfileHasNoBareOption(t *testing.T) {
	for _, inst := range []Instance{
		{Id: 1, Port: 1194},
		{Id: 2, Port: 443, UseTCP: true},
	} {
		out := profileFor(t, inst, Profile{Address: "vpn.example.com", Label: "Germany"})
		if bare := findBareOptions(out); len(bare) > 0 {
			t.Errorf("inbound %d: the client refuses this profile outright: %q\n%s", inst.Id, bare, out)
		}
	}
}

// The server config has the same way of going wrong, and it fails later and
// more quietly: the process exits, the inbound is simply absent, and the
// operator is left with a protocol that does not come up.
func TestTheServerConfigHasNoBareOption(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 1194, DNS: []string{"1.1.1.1"}})
	if bare := findBareOptions(cfg); len(bare) > 0 {
		t.Errorf("the server would refuse to start: %q\n%s", bare, cfg)
	}
}

// Without this the OpenVPN clients stop and ask the customer to choose a
// client certificate out of their phone's keychain — a question they cannot
// answer, on a profile that deliberately has none.
func TestTheClientIsToldThereIsNoCertificateToPick(t *testing.T) {
	out := profileFor(t, Instance{Id: 1, Port: 1194}, Profile{Address: "198.51.100.1"})
	if !strings.Contains(out, "setenv CLIENT_CERT 0") {
		t.Fatalf("the customer would be asked to pick a certificate they do not have:\n%s", out)
	}
}

// The check on the server's certificate is remote-cert-tls, and nothing else.
// verify-x509-name pins a subject name that this panel never fixes — it
// reissues nothing and promises no particular common name — so a profile
// carrying one would start failing the day a certificate is regenerated.
func TestTheServerCertificateIsCheckedByItsPurposeNotItsName(t *testing.T) {
	out := profileFor(t, Instance{Id: 1, Port: 1194}, Profile{Address: "198.51.100.1"})
	if !strings.Contains(out, "remote-cert-tls server") {
		t.Errorf("the client would accept any certificate it is handed:\n%s", out)
	}
	if strings.Contains(out, "verify-x509-name") {
		t.Errorf("verify-x509-name is back in the profile:\n%s", out)
	}
}
