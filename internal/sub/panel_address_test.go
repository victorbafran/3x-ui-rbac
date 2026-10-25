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
