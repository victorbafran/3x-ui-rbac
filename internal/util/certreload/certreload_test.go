package certreload

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type pair struct{ cert, key []byte }

func newPair(t *testing.T, serial int64) pair {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "panel.example.ir"},
		DNSNames:     []string{"panel.example.ir"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pair{
		cert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		key:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}),
	}
}

func write(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func serial(t *testing.T, c *tls.Certificate) int64 {
	t.Helper()
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.Int64()
}

// loaderAt starts a loader on pair a with a clock the test moves by hand.
func loaderAt(t *testing.T, a pair) (*Loader, string, string, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	write(t, certFile, a.cert)
	write(t, keyFile, a.key)
	l, err := New(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	l.now = func() time.Time { return clock }
	l.checked = clock
	return l, certFile, keyFile, &clock
}

// A certificate renewed at the same paths is served from the next check on,
// without restarting the panel or the subscription server.
func TestARenewedCertificateIsServedWithoutARestart(t *testing.T) {
	l, certFile, keyFile, clock := loaderAt(t, newPair(t, 1))
	b := newPair(t, 2)
	write(t, certFile, b.cert)
	write(t, keyFile, b.key)

	got, _ := l.GetCertificate(nil)
	if serial(t, got) != 1 {
		t.Errorf("served serial %d before the check was due; the files are read at most every %s", serial(t, got), checkEvery)
	}
	*clock = clock.Add(checkEvery)
	got, _ = l.GetCertificate(nil)
	if serial(t, got) != 2 {
		t.Errorf("served serial %d after the renewal, want 2", serial(t, got))
	}
}

// A renewal caught between its two renames is a new certificate with the old
// key; that pair does not load, so the old certificate stays until it does.
func TestAHalfWrittenRenewalKeepsTheOldCertificate(t *testing.T) {
	l, certFile, keyFile, clock := loaderAt(t, newPair(t, 1))
	b := newPair(t, 2)
	write(t, certFile, b.cert)

	*clock = clock.Add(checkEvery)
	got, err := l.GetCertificate(nil)
	if err != nil || serial(t, got) != 1 {
		t.Fatalf("served %v, err %v; want the old certificate kept", got, err)
	}
	write(t, keyFile, b.key)
	*clock = clock.Add(checkEvery)
	got, _ = l.GetCertificate(nil)
	if serial(t, got) != 2 {
		t.Errorf("the completed renewal was not picked up: serial %d", serial(t, got))
	}
}

// A pair that cannot be read at start fails like tls.LoadX509KeyPair, so the
// server still falls back to plain HTTP and says why.
func TestAPairThatWillNotLoadFailsAtStart(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(filepath.Join(dir, "none.pem"), filepath.Join(dir, "none.key")); err == nil {
		t.Fatal("a missing pair was accepted")
	}
	a, b := newPair(t, 1), newPair(t, 2)
	write(t, filepath.Join(dir, "c.pem"), a.cert)
	write(t, filepath.Join(dir, "k.pem"), b.key)
	if _, err := New(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")); err == nil {
		t.Fatal("a certificate with someone else's key was accepted")
	}
}
