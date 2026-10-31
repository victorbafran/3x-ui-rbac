package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// certFor writes a self-signed certificate valid for names and returns its path.
func certFor(t *testing.T, names ...string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     names,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "vpn.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	return path
}

// A cisco inbound handing out IPv6 by name needs its certificate to carry that
// name, or AnyConnect refuses it; the form asks about the file the inbound uses.
func TestCertCoversNamesWhatACiscoCertificateDoesNotCarry(t *testing.T) {
	setupBulkDB(t)
	cert := certFor(t, "vpn.example.com", "*.v6.example.com")
	mkInbound(t, 4443, model.Cisco, `{"clients":[],"certFile":"`+filepath.ToSlash(cert)+`"}`)

	got, err := (&ServerService{}).CertCovers(cert, []string{"a.v6.example.com", "ipv6.example.com", "[2001:db8::5]", " "})
	if err != nil {
		t.Fatalf("CertCovers: %v", err)
	}
	if got.Host != "vpn.example.com" {
		t.Errorf("host = %q, want the name the certificate was issued for", got.Host)
	}
	if want := []string{"ipv6.example.com", "2001:db8::5"}; !slices.Equal(got.Uncovered, want) {
		t.Errorf("uncovered = %q, want %q", got.Uncovered, want)
	}
}

// Only files the panel already points at are read: a path from the request
// alone would let the route probe any file on the server.
func TestCertCoversRefusesAFileNoInboundUses(t *testing.T) {
	setupBulkDB(t)
	cert := certFor(t, "vpn.example.com")
	if _, err := (&ServerService{}).CertCovers(cert, []string{"vpn.example.com"}); !errors.Is(err, ErrCertNotReferenced) {
		t.Fatalf("err = %v, want ErrCertNotReferenced for a file no inbound or setting references", err)
	}
}
