package sub

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func panelCert(t *testing.T, name string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fullchain.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func saveSetting(t *testing.T, key, value string) {
	t.Helper()
	if err := database.GetDB().Save(&model.Setting{Key: key, Value: value}).Error; err != nil {
		t.Fatalf("save %s: %v", key, err)
	}
}

// Seen on 2026-10-25: with both certificates issued, a config with no address
// of its own pointed at the subscription domain instead of the panel's.
func TestAConfigWithNoAddressGoesToThePanel(t *testing.T) {
	initSubDB(t)
	local := &model.Inbound{Listen: "", ShareAddrStrategy: "node"}
	saveSetting(t, "webCertFile", panelCert(t, "ger.example.ir"))
	saveSetting(t, "subDomain", "sub.example.ir")

	s := (&SubService{}).ForRequest("sub.example.ir")
	if got := s.resolveInboundAddress(local); got != "ger.example.ir" {
		t.Errorf("a config with no address went to %q, want the panel's ger.example.ir", got)
	}
	if subURL, _, _ := s.BuildURLs("/sub/", "/json/", "/clash/", "ABC"); !strings.Contains(subURL, "sub.example.ir") {
		t.Errorf("the subscription link itself moved off its own domain: %q", subURL)
	}
	custom := &model.Inbound{ShareAddrStrategy: "custom", ShareAddr: "212.74.39.76"}
	if got := s.resolveInboundAddress(custom); got != "212.74.39.76" {
		t.Errorf("an inbound's own address lost to the panel's: %q", got)
	}

	saveSetting(t, "webDomain", "panel.example.ir")
	s = (&SubService{}).ForRequest("sub.example.ir")
	if got := s.resolveInboundAddress(local); got != "panel.example.ir" {
		t.Errorf("the panel's domain setting lost to its certificate: %q", got)
	}
}

// A TLS client checks the name it dials against the certificate, so an
// inbound with a certificate of its own goes to the name on it.
func TestAnInboundWithItsOwnCertificateGoesToItsName(t *testing.T) {
	initSubDB(t)
	saveSetting(t, "webCertFile", panelCert(t, "ger.example.ir"))
	// Forward slashes: a Windows path pasted into JSON would carry bare backslashes.
	own := filepath.ToSlash(panelCert(t, "212.example.ir"))
	tlsStream := `{"network":"tcp","security":"tls","tlsSettings":{"certificates":[{"certificateFile":"` + own + `"}]}}`
	for _, tc := range []struct {
		why  string
		ib   *model.Inbound
		want string
	}{
		{"cisco with its own certificate", &model.Inbound{Id: 6, Protocol: model.Cisco, Settings: `{"certFile":"` + own + `"}`}, "212.example.ir"},
		{"cisco with a generated one", &model.Inbound{Id: 1, Protocol: model.Cisco, Settings: `{}`}, "ger.example.ir"},
		{"xray over TLS", &model.Inbound{Id: 8, Protocol: model.VLESS, Settings: `{}`, StreamSettings: tlsStream}, "212.example.ir"},
		{"xray moved off TLS", &model.Inbound{Id: 2, Protocol: model.VLESS, Settings: `{}`, StreamSettings: strings.Replace(tlsStream, `"tls"`, `"reality"`, 1)}, "ger.example.ir"},
		{"an address of its own still wins", &model.Inbound{Id: 9, Protocol: model.Cisco, Settings: `{"certFile":"` + own + `"}`, ShareAddrStrategy: "custom", ShareAddr: "212.74.39.76"}, "212.74.39.76"},
	} {
		s := (&SubService{}).ForRequest("sub.example.ir")
		if got := s.resolveInboundAddress(tc.ib); got != tc.want {
			t.Errorf("%s: address %q, want %q", tc.why, got, tc.want)
		}
	}
}
