package certissue

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// isolate points the package at a temporary directory and at an ACME
// directory nothing answers, so a test that reaches the network by mistake
// fails at once instead of asking Let's Encrypt for a certificate.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	savedBase, savedURL := baseDirOverride, directoryURL
	baseDirOverride = dir
	directoryURL = "http://127.0.0.1:1/directory"
	t.Cleanup(func() { baseDirOverride, directoryURL = savedBase, savedURL })
	return dir
}

// The names are what Let's Encrypt is asked to prove, and what lands in the
// certificate a phone compares against the address it typed.
func TestNamesAreNormalisedKeepingTheirOrder(t *testing.T) {
	got, err := NormalizeNames([]string{" TUN.Example.ir. ", "212.example.ir", "tun.example.ir", ""})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tun.example.ir", "212.example.ir"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// HTTP-01 proves a name by fetching a file. It cannot prove an address or a
// wildcard, and asking anyway spends one of Let's Encrypt's failed-attempt
// allowances on a request that was never going to work.
func TestWhatPortEightyCannotProveIsRefusedBeforeAsking(t *testing.T) {
	for _, bad := range [][]string{
		{"212.95.35.41"},
		{"*.example.ir"},
		{"localhost"},
		{"has space.example.ir"},
		{"-bad.example.ir"},
		{},
		{"  "},
	} {
		if _, err := NormalizeNames(bad); err == nil {
			t.Errorf("NormalizeNames(%q) accepted it", bad)
		}
	}
}

// The challenge port is open to the internet while an order runs. It must
// answer the proof and nothing else.
func TestTheChallengeServerAnswersOnlyItsOwnProof(t *testing.T) {
	c := newChallengeServer()
	c.set("/.well-known/acme-challenge/tok", "tok.thumb")
	srv := httptest.NewServer(c)
	defer srv.Close()

	get := func(path string) (int, string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := make([]byte, 64)
		n, _ := resp.Body.Read(b)
		return resp.StatusCode, string(b[:n])
	}
	if code, body := get("/.well-known/acme-challenge/tok"); code != http.StatusOK || body != "tok.thumb" {
		t.Fatalf("the proof came back as %d %q", code, body)
	}
	for _, p := range []string{"/", "/.well-known/acme-challenge/other", "/panel/"} {
		if code, _ := get(p); code != http.StatusNotFound {
			t.Errorf("%s answered %d; nothing but the proof may answer", p, code)
		}
	}
}

// Whatever already holds port 80 has to be named as the problem. Without
// this the operator reads a Let's Encrypt timeout and goes looking at DNS.
func TestATakenPortIsReportedAsTaken(t *testing.T) {
	held, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	_, err = newChallengeServer().listen(context.Background(), held.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "port 80 is taken") {
		t.Fatalf("a taken port came back as %v", err)
	}
}

// The key is the one file on this box that would let anybody impersonate
// the VPN server, so it is written for root alone, and a reader never meets
// half a file.
func TestFilesAreWrittenWithTheKeyPrivate(t *testing.T) {
	dir := filepath.Join(isolate(t), "tun.example.ir")
	chain, key := selfSigned(t, "tun.example.ir", time.Now().Add(80*24*time.Hour))
	meta := Meta{Names: []string{"tun.example.ir"}, NotAfter: time.Now().Add(80 * 24 * time.Hour).UTC()}
	if err := writeFiles(dir, chain, key, meta); err != nil {
		t.Fatal(err)
	}
	if !pairExists(dir) {
		t.Fatal("the pair was written but is not found")
	}
	got, err := readMeta(dir)
	if err != nil || got.Names[0] != "tun.example.ir" {
		t.Fatalf("the record did not round-trip: %+v %v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, keyFileName))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("the key is %v, want 0600", info.Mode().Perm())
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(matches) != 0 {
		t.Fatalf("temporary files were left behind: %v", matches)
	}
}

// Pressing the button twice must not spend a second certificate from Let's
// Encrypt's weekly allowance. The test's ACME directory answers nothing, so
// reaching the network here would fail.
func TestACertificateWithLifeLeftIsHandedBack(t *testing.T) {
	base := isolate(t)
	names := []string{"tun.example.ir", "212.example.ir"}
	dir := filepath.Join(base, names[0])
	notAfter := time.Now().Add(60 * 24 * time.Hour)
	chain, key := selfSigned(t, names[0], notAfter)
	if err := writeFiles(dir, chain, key, Meta{Names: names, NotAfter: notAfter.UTC()}); err != nil {
		t.Fatal(err)
	}

	res, err := Issue(context.Background(), []string{"TUN.example.ir", "212.example.ir"})
	if err != nil {
		t.Fatalf("a fresh certificate for the same names was not reused: %v", err)
	}
	if !res.Reused || res.CertFile != filepath.Join(dir, certFileName) || res.KeyFile != filepath.Join(dir, keyFileName) {
		t.Fatalf("unexpected result %+v", res)
	}
}

// Fewer names, more names, or a month from expiry: each of those needs a new
// certificate, and handing back the old one would leave a customer with a
// name the certificate does not vouch for.
func TestADifferentOrExpiringCertificateIsNotHandedBack(t *testing.T) {
	base := isolate(t)
	dir := filepath.Join(base, "tun.example.ir")

	chain, key := selfSigned(t, "tun.example.ir", time.Now().Add(60*24*time.Hour))
	if err := writeFiles(dir, chain, key, Meta{Names: []string{"tun.example.ir"}, NotAfter: time.Now().Add(60 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := Issue(context.Background(), []string{"tun.example.ir", "212.example.ir"}); err == nil {
		t.Fatal("a certificate missing one of the names was handed back")
	}

	if err := writeFiles(dir, chain, key, Meta{Names: []string{"tun.example.ir"}, NotAfter: time.Now().Add(10 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := Issue(context.Background(), []string{"tun.example.ir"}); err == nil {
		t.Fatal("a certificate ten days from expiry was handed back")
	}
}

// Renewal runs a month ahead, not on the day.
func TestRenewalIsDueAMonthBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	if dueForRenewal(now.Add(31*24*time.Hour), now) {
		t.Error("31 days left was renewed already")
	}
	if !dueForRenewal(now.Add(30*24*time.Hour), now) {
		t.Error("30 days left was not renewed")
	}
	if !dueForRenewal(now.Add(-time.Hour), now) {
		t.Error("an expired certificate was not renewed")
	}
}

// Renewal touches only what the panel issued and only what is due: a
// directory the operator made by hand, or a certificate with months left, is
// left alone.
func TestRenewalTouchesOnlyWhatIsDue(t *testing.T) {
	base := isolate(t)
	if err := os.MkdirAll(filepath.Join(base, "handmade.example.ir"), 0o700); err != nil {
		t.Fatal(err)
	}
	fresh, key := selfSigned(t, "fresh.example.ir", time.Now().Add(80*24*time.Hour))
	if err := writeFiles(filepath.Join(base, "fresh.example.ir"), fresh, key,
		Meta{Names: []string{"fresh.example.ir"}, NotAfter: time.Now().Add(80 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	old, key2 := selfSigned(t, "old.example.ir", time.Now().Add(5*24*time.Hour))
	if err := writeFiles(filepath.Join(base, "old.example.ir"), old, key2,
		Meta{Names: []string{"old.example.ir"}, NotAfter: time.Now().Add(5 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	errs := RenewDue(context.Background(), time.Now())
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "old.example.ir") {
		t.Fatalf("want exactly the due certificate attempted, got %v", errs)
	}
}

// One account per panel, kept across issuances.
func TestTheAccountKeyIsKept(t *testing.T) {
	isolate(t)
	a, err := loadOrCreateAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := loadOrCreateAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	if !a.Equal(b) {
		t.Fatal("a second issuance made a second account")
	}
}

func selfSigned(t *testing.T, name string, notAfter time.Time) ([][]byte, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return [][]byte{der}, key
}
