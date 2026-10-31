package openvpn

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// inboundNamingACertificate is an inbound saved while the form still offered
// certificate fields, pointing at a Let's Encrypt pair on disk.
func inboundNamingACertificate(t *testing.T) (*model.Inbound, string) {
	t.Helper()
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	le := t.TempDir()
	fullchain, privkey := filepath.Join(le, "fullchain.pem"), filepath.Join(le, "privkey.pem")
	for _, p := range []string{fullchain, privkey} {
		if err := os.WriteFile(p, []byte("issued by lets encrypt "+p), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	settings := fmt.Sprintf(`{"clients":[{"email":"a@b","password":"pw","enable":true}],"certFile":%q,"keyFile":%q}`,
		fullchain, privkey)
	return &model.Inbound{Id: 3, Tag: "inbound-1194", Port: 1194, Protocol: model.OpenVPN, Settings: settings}, fullchain
}

func configLine(t *testing.T, cfg, directive string) string {
	t.Helper()
	for _, l := range strings.Split(cfg, "\n") {
		if v, ok := strings.CutPrefix(l, directive+" "); ok {
			return v
		}
	}
	t.Fatalf("the config has no %s line:\n%s", directive, cfg)
	return ""
}

func readCert(t *testing.T, pemBody []byte, what string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemBody)
	if block == nil {
		t.Fatalf("%s is not a PEM certificate", what)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return cert
}

// Seen live: an inbound naming its own certificate got `ca ca-3.pem`, a file
// nobody made, so openvpn exited every 10 s and the .ovpn answered 503.
func TestAnInboundNamingACertificateServesThePanelsOwn(t *testing.T) {
	ib, _ := inboundNamingACertificate(t)
	inst, ok := InstanceFromInbound(ib)
	if !ok {
		t.Fatal("the inbound was not served at all")
	}
	if _, _, _, err := ensureKeypair(inst); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTLSCryptKey(inst.Id); err != nil {
		t.Fatal(err)
	}
	cfg := renderConfig(inst, HelperCommand("/usr/local/x-ui/x-ui", inst.Id), testDrop)

	files := map[string][]byte{}
	for _, directive := range []string{"ca", "cert", "key"} {
		path := configLine(t, cfg, directive)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("openvpn would exit at startup: --%s names %s: %v", directive, path, err)
		}
		files[directive] = body
	}

	profile, err := Render(inst, Profile{Address: "vpn.example.com"})
	if err != nil {
		t.Fatalf("the customer's .ovpn cannot be made: %v", err)
	}
	if !strings.Contains(profile, string(files["ca"])) {
		t.Fatal("the customer's file carries a different authority from the one the server is told to use")
	}
	ca := readCert(t, files["ca"], "the authority")
	server := readCert(t, files["cert"], "the server certificate")
	if err := server.CheckSignatureFrom(ca); err != nil {
		t.Fatalf("the server's certificate is not signed by the authority the customer trusts: %v", err)
	}
	if _, err := tls.X509KeyPair(files["cert"], files["key"]); err != nil {
		t.Fatalf("openvpn would refuse a certificate and key that are not one pair: %v", err)
	}
}

// The renewal job rewrites the Let's Encrypt file every two months; the
// server no longer reads it, so that must not drop every live session.
func TestARenewedCertificateTheInboundNamesDoesNotRestartIt(t *testing.T) {
	ib, fullchain := inboundNamingACertificate(t)
	inst, _ := InstanceFromInbound(ib)
	if _, _, _, err := ensureKeypair(inst); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureTLSCryptKey(inst.Id); err != nil {
		t.Fatal(err)
	}
	before := inst.structuralFingerprint()

	if err := os.WriteFile(fullchain, []byte("renewed, longer than before"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, _ := InstanceFromInbound(ib)
	if got := again.structuralFingerprint(); got != before {
		t.Errorf("a certificate the server does not read restarted it:\nbefore %s\nafter  %s", before, got)
	}
}
