package tunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func wssPair() Pair {
	p := goodPair()
	p.Transport = TransportWSSMux
	return p
}

// lookedAtTheConfiguredPair fails unless the check run on the front named the
// very files the leg's server config hands Backhaul, since a check of any
// other path would call a pair present that Backhaul cannot load.
func lookedAtTheConfiguredPair(t *testing.T, r *fakeRemote) {
	t.Helper()
	config, err := ServerConfig(Legs(wssPair())[0])
	if err != nil {
		t.Fatal(err)
	}
	const cert, key = "/etc/backhaul/x-ui-2.crt", "/etc/backhaul/x-ui-2.key"
	if !strings.Contains(config, `tls_cert = "`+cert+`"`) || !strings.Contains(config, `tls_key = "`+key+`"`) {
		t.Fatalf("the server config names another pair:\n%s", config)
	}
	for _, line := range r.cmdLines {
		if strings.Contains(line, "'"+cert+"'") && strings.Contains(line, "'"+key+"'") {
			return
		}
	}
	t.Errorf("no check looked at %s and %s: %q", cert, key, r.cmdLines)
}

func (f *fakeRemote) count(mode string) int {
	n := 0
	for _, c := range f.calls {
		if c.mode == mode {
			n++
		}
	}
	return n
}

// The Iran front on wssmux died with "open : no such file or directory" in a
// restart loop: Backhaul's wss server cannot listen until a pair is on disk.
func TestAMissingCertificateIsSentBeforeTheTunnelStarts(t *testing.T) {
	r := fakeRemote{runOut: "missing\n"}
	if err := provisionLegs(t.Context(), &r, wssPair(), ""); err != nil {
		t.Fatal(err)
	}
	steps := strings.Split(r.order(), ", ")
	at := func(step string) int {
		i := slices.Index(steps, step)
		if i < 0 {
			t.Fatalf("%q never happened: %s", step, r.order())
		}
		return i
	}
	lookedAtTheConfiguredPair(t, &r)
	staged, started := at("stage backhaul-x-ui-2-udp"), at("apply backhaul-x-ui-2")
	for _, sent := range []string{"upload /etc/backhaul/x-ui-2.key", "upload /etc/backhaul/x-ui-2.crt"} {
		if i := at(sent); i < staged || i > started {
			t.Errorf("%s is not between staging and starting the leg: %s", sent, r.order())
		}
	}

	certPEM, keyPEM := r.uploaded["/etc/backhaul/x-ui-2.crt"], r.uploaded["/etc/backhaul/x-ui-2.key"]
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("the pair sent does not load the way Backhaul loads it: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	// Nothing renews it, so a short one would take the tunnel down on expiry.
	if time.Until(leaf.NotAfter) < 9*365*24*time.Hour {
		t.Errorf("the certificate expires %s; nothing renews it", leaf.NotAfter)
	}

	block, _ := pem.Decode(keyPEM)
	if block == nil {
		t.Fatal("the key sent is not PEM")
	}
	keyBody := base64.StdEncoding.EncodeToString(block.Bytes)[:40]
	keyB64 := base64.StdEncoding.EncodeToString(keyPEM)[:40]
	for _, line := range r.cmdLines {
		if strings.Contains(line, keyBody) || strings.Contains(line, keyB64) {
			t.Errorf("the private key crossed on a command line, where any process list shows it: %.80s", line)
		}
	}
}

// The reconcile provisions every front each minute; a pair already there must
// survive it, or the front's certificate would change under live tunnels.
func TestAPresentCertificateIsLeftAlone(t *testing.T) {
	r := fakeRemote{runOut: "present\n"}
	if err := provisionLegs(t.Context(), &r, wssPair(), ""); err != nil {
		t.Fatal(err)
	}
	if r.count("run") != 1 {
		t.Errorf("looked for the pair %d times, want once: %s", r.count("run"), r.order())
	}
	lookedAtTheConfiguredPair(t, &r)
	if len(r.uploaded) != 0 {
		t.Errorf("a pair already on the front was replaced: %s", r.order())
	}
}

// BackPack makes its own certificate and plain transports need none, so those
// fronts must not pay an extra SSH round trip every minute.
func TestOnlyABackhaulWSSFrontIsCheckedForACertificate(t *testing.T) {
	backpackWSS := backpackPair()
	backpackWSS.Transport = TransportWSS
	plainWS := goodPair()
	plainWS.Transport = TransportWS
	for name, p := range map[string]Pair{"backhaul tcp": goodPair(), "backhaul ws": plainWS, "backpack wss": backpackWSS} {
		r := fakeRemote{runOut: "missing\n"}
		if err := provisionLegs(t.Context(), &r, p, ""); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.count("run") != 0 || len(r.uploaded) != 0 {
			t.Errorf("%s was given a certificate it does not use: %s", name, r.order())
		}
	}
}

// diskRemote is a front whose /etc/backhaul is dir: the check runs in a real
// shell over real files, so what it reads is what the front would read.
type diskRemote struct {
	t   *testing.T
	sh  string
	dir string
}

func newDiskRemote(t *testing.T) *diskRemote {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to run the check with")
	}
	return &diskRemote{t: t, sh: sh, dir: filepath.ToSlash(t.TempDir())}
}

func (d *diskRemote) local(path string) string {
	return strings.Replace(path, "/etc/backhaul", d.dir, 1)
}

func (d *diskRemote) Run(_ context.Context, command string) (string, error) {
	out, err := exec.Command(d.sh, "-c", strings.ReplaceAll(command, "/etc/backhaul", d.dir)).Output()
	return string(out), err
}

func (d *diskRemote) Upload(_ context.Context, path string, data []byte) error {
	return os.WriteFile(d.local(path), data, 0o600)
}

func (d *diskRemote) RunScript(context.Context, string, string, ...string) (string, error) {
	return "", nil
}

func (d *diskRemote) write(path string, data []byte) {
	d.t.Helper()
	if err := os.WriteFile(d.local(path), data, 0o600); err != nil {
		d.t.Fatal(err)
	}
}

func (d *diskRemote) read(path string) []byte {
	d.t.Helper()
	data, err := os.ReadFile(d.local(path))
	if err != nil {
		d.t.Fatal(err)
	}
	return data
}

func (d *diskRemote) check(leg Pair) string {
	d.t.Helper()
	out, err := d.Run(d.t.Context(), tlsPairCheck(leg.remoteCertPath(), leg.remoteKeyPath()))
	if err != nil {
		d.t.Fatalf("the check failed to run: %v", err)
	}
	return lastLine(out)
}

// Backhaul cannot load an empty file, half a file, or a key that is not the
// certificate's, and every one of those is what a cut upload or two runs at
// once leave behind. The check must call each missing, so a fresh pair goes up.
func TestTheCertificateCheckWantsOneWholePair(t *testing.T) {
	d := newDiskRemote(t)
	leg := Legs(wssPair())[0]
	cert, key := leg.remoteCertPath(), leg.remoteKeyPath()
	certA, keyA, err := selfSignedPair()
	if err != nil {
		t.Fatal(err)
	}
	_, keyB, err := selfSignedPair()
	if err != nil {
		t.Fatal(err)
	}

	if got := d.check(leg); got != "missing" {
		t.Errorf("neither file: %q", got)
	}
	d.write(cert, certA)
	d.write(key, keyA)
	if got := d.check(leg); got != "present" {
		t.Errorf("a whole pair: %q", got)
	}
	for name, files := range map[string][2][]byte{
		"an empty certificate":          {nil, keyA},
		"an empty key":                  {certA, nil},
		"a certificate cut short":       {certA[:len(certA)/2], keyA},
		"a key cut short":               {certA, keyA[:len(keyA)-2]},
		"a key from another run":        {certA, keyB},
		"two files that are not a pair": {[]byte("c\n"), []byte("c\n")},
	} {
		d.write(cert, files[0])
		d.write(key, files[1])
		if got := d.check(leg); got != "missing" {
			t.Errorf("%s: %q", name, got)
		}
	}
	if err := os.Remove(d.local(cert)); err != nil {
		t.Fatal(err)
	}
	d.write(key, keyA)
	if got := d.check(leg); got != "missing" {
		t.Errorf("no certificate: %q", got)
	}
}

// Two runs at once once left the key of one and the certificate of the other,
// which Backhaul refuses for ever. The next pass must replace such a pair with
// one that loads, and the pass after it must leave that one alone.
func TestAMismatchedPairIsReplacedOnTheNextPass(t *testing.T) {
	d := newDiskRemote(t)
	leg := Legs(wssPair())[0]
	certA, _, err := selfSignedPair()
	if err != nil {
		t.Fatal(err)
	}
	_, keyB, err := selfSignedPair()
	if err != nil {
		t.Fatal(err)
	}
	d.write(leg.remoteCertPath(), certA)
	d.write(leg.remoteKeyPath(), keyB)

	if err := ensureTLSPair(t.Context(), d, leg); err != nil {
		t.Fatal(err)
	}
	cert, key := d.read(leg.remoteCertPath()), d.read(leg.remoteKeyPath())
	if _, err := tls.X509KeyPair(cert, key); err != nil {
		t.Fatalf("the pair left on the front does not load: %v", err)
	}
	if err := ensureTLSPair(t.Context(), d, leg); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.read(leg.remoteKeyPath()), key) || !bytes.Equal(d.read(leg.remoteCertPath()), cert) {
		t.Error("a whole pair was replaced on the next pass")
	}
}
