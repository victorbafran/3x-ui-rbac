package certreload

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func certFor(t *testing.T, names []string, ips ...string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "cn.example.com"},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	for _, ip := range ips {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP(ip))
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

// A config with no address of its own is sent to the panel's name, which is
// the name its certificate was issued for.
func TestTheHostIsTheNameTheCertificateWasIssuedFor(t *testing.T) {
	for _, tc := range []struct {
		why   string
		names []string
		ips   []string
		want  string
	}{
		{"one domain", []string{"ger.example.ir"}, nil, "ger.example.ir"},
		{"the first of several", []string{"ger.example.ir", "sub.example.ir"}, nil, "ger.example.ir"},
		{"a wildcard cannot be dialled", []string{"*.example.ir", "ger.example.ir"}, nil, "ger.example.ir"},
		{"localhost is no address", []string{"localhost", "ger.example.ir"}, nil, "ger.example.ir"},
		{"nor is a bare label", []string{"in-8443-tcp", "ger.example.ir"}, nil, "ger.example.ir"},
		{"a domain before an IP", []string{"ger.example.ir"}, []string{"212.95.35.41"}, "ger.example.ir"},
		{"an IP certificate", nil, []string{"127.0.0.1", "10.0.0.2", "212.95.35.41"}, "212.95.35.41"},
		{"nothing a client can reach", []string{"*.example.ir"}, []string{"127.0.0.1"}, ""},
	} {
		if got := Host(certFor(t, tc.names, tc.ips...)); got != tc.want {
			t.Errorf("%s: Host = %q, want %q", tc.why, got, tc.want)
		}
	}
}

func TestNoCertificateMeansNoHost(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "missing.pem"), junk} {
		if got := Host(path); got != "" {
			t.Errorf("Host(%s) = %q, want nothing", filepath.Base(path), got)
		}
	}
}
