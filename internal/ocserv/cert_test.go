package ocserv

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// openTempBase returns a temporary directory whose whole path is traversable.
// t.TempDir() hands back something under a 0700 parent, which the checker
// under test correctly objects to — the point here is what the code creates,
// not what the test framework did above it.
func openTempBase(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for p := dir; p != "/" && p != "."; p = filepath.Dir(p) {
		st, err := os.Stat(p)
		if err != nil {
			break
		}
		if st.Mode().Perm()&0o001 == 0 {
			if err := os.Chmod(p, st.Mode().Perm()|0o011); err != nil {
				t.Fatalf("chmod %s: %v", p, err)
			}
		}
	}
	return dir
}

func withTempRuntime(t *testing.T) {
	t.Helper()
	runtimeDirOverride = openTempBase(t)
	t.Cleanup(func() { runtimeDirOverride = "" })
}

// Cisco's own Secure Client cannot complete a handshake against an ECDSA
// server key: it gives up before the certificate is sent, so the server reads
// as dead rather than untrusted. ocserv and openconnect are both happy with
// either, which is exactly what lets the mistake survive testing.
func TestGeneratedKeyIsRSASoTheCiscoClientCanConnect(t *testing.T) {
	withTempRuntime(t)
	certFile, keyFile, err := ensureKeypair(Instance{Id: 1, Tag: "cisco-1"})
	if err != nil {
		t.Fatalf("ensureKeypair: %v", err)
	}
	raw, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("the certificate file is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	if cert.PublicKeyAlgorithm != x509.RSA {
		t.Fatalf("server key is %v; the real Cisco client cannot handshake against anything but RSA", cert.PublicKeyAlgorithm)
	}

	// The key is read by the privileged main process only, the certificate by
	// the unprivileged worker.
	st, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if mode := st.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("private key mode is %o; it is readable beyond its owner", mode)
	}
}

// Regenerating on every reconcile would hand clients a different server
// identity each time the panel restarted.
func TestKeypairIsReusedOnceGenerated(t *testing.T) {
	withTempRuntime(t)
	certFile, _, err := ensureKeypair(Instance{Id: 2})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	first, _ := os.ReadFile(certFile)
	if _, _, err := ensureKeypair(Instance{Id: 2}); err != nil {
		t.Fatalf("second: %v", err)
	}
	second, _ := os.ReadFile(certFile)
	if string(first) != string(second) {
		t.Fatal("the certificate was regenerated; every client would see a new server identity")
	}
}

// An operator-supplied pair is used untouched — the panel must not quietly
// serve a self-signed certificate in place of the one they configured.
func TestOperatorKeypairIsUsedAsGiven(t *testing.T) {
	withTempRuntime(t)
	certFile, keyFile, err := ensureKeypair(Instance{Id: 3, CertFile: "/etc/ssl/mine.pem", KeyFile: "/etc/ssl/mine.key"})
	if err != nil {
		t.Fatalf("ensureKeypair: %v", err)
	}
	if certFile != "/etc/ssl/mine.pem" || keyFile != "/etc/ssl/mine.key" {
		t.Fatalf("got %s / %s, want the configured pair", certFile, keyFile)
	}
	if fileExists(certPathForID(3)) {
		t.Fatal("a self-signed certificate was generated although one was configured")
	}
}

// Disabling an inbound keeps its identity; deleting it takes the key with it.
func TestForgetDropsTheKeypairAndRemoveKeepsIt(t *testing.T) {
	withTempRuntime(t)
	if err := ensureRuntimeDir(); err != nil {
		t.Fatalf("runtime dir: %v", err)
	}
	if _, _, err := ensureKeypair(Instance{Id: 7}); err != nil {
		t.Fatalf("ensureKeypair: %v", err)
	}
	// Remove walks away immediately when the id is not being managed, so the
	// instance has to be in the map or this test passes without ever reaching
	// the code it is about. The process was never started, which Stop reports
	// and removeLocked ignores.
	m := &Manager{procs: map[int]*managed{
		7: {proc: newProcess(configPathForID(7), "cisco-7"), cidr: ""},
	}, swept: true}

	m.Remove(7)
	if _, still := m.procs[7]; still {
		t.Fatal("Remove left the instance in the managed set; the rest of this test would prove nothing")
	}
	if !fileExists(keyPathForID(7)) {
		t.Fatal("disabling an inbound threw its certificate away; re-enabling it would look like a different server")
	}

	m.procs[7] = &managed{proc: newProcess(configPathForID(7), "cisco-7"), cidr: ""}
	m.Forget(7)
	if fileExists(keyPathForID(7)) || fileExists(certPathForID(7)) {
		t.Fatal("a deleted inbound left its private key on disk")
	}
}
