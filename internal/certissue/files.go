package certissue

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/web/network"
)

const (
	certFileName = "fullchain.pem"
	keyFileName  = "privkey.pem"
	metaFileName = "meta.json"
	accountFile  = "account.key"
)

// Meta is what the panel remembers about a certificate it issued, kept
// beside the files so renewal knows what to ask for again.
type Meta struct {
	Names    []string  `json:"names"`
	NotAfter time.Time `json:"notAfter"`
	Issued   time.Time `json:"issued"`
}

func readMeta(dir string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(filepath.Join(dir, metaFileName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if len(m.Names) == 0 {
		return m, errors.New("the certificate's record names nothing")
	}
	return m, nil
}

func pairExists(dir string) bool {
	for _, f := range []string{certFileName, keyFileName} {
		if info, err := os.Stat(filepath.Join(dir, f)); err != nil || info.Size() == 0 {
			return false
		}
	}
	return true
}

// writeFiles lays down the chain, the key and the record. Each file is
// written beside its final name and renamed over it, so a reader never sees
// half a certificate. The record goes last: a crash part way leaves the old
// record, and renewal simply runs again.
func writeFiles(dir string, chain [][]byte, key *rsa.PrivateKey, meta Meta) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("could not make %s: %w", dir, err)
	}
	var certPEM []byte
	for _, der := range chain {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{certFileName, certPEM, 0o644},
		{keyFileName, keyPEM, 0o600},
		{metaFileName, append(metaJSON, '\n'), 0o600},
	}
	for _, f := range files {
		if err := writeAtomic(filepath.Join(dir, f.name), f.data, f.mode); err != nil {
			return err
		}
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("could not write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("could not put %s in place: %w", path, err)
	}
	return nil
}

// loadOrCreateAccountKey keeps one Let's Encrypt account per panel. A new
// account per certificate would work too, but Let's Encrypt counts
// registrations, and an account it has seen before is what lets a name
// proved yesterday skip today's check.
func loadOrCreateAccountKey() (*ecdsa.PrivateKey, error) {
	path := filepath.Join(baseDir(), accountFile)
	if b, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, fmt.Errorf("%s is not a PEM key; remove it and the panel will make a new account", path)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s does not parse; remove it and the panel will make a new account: %w", path, err)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("could not read %s: %w", path, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("no randomness for an account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(baseDir(), 0o700); err != nil {
		return nil, fmt.Errorf("could not make %s: %w", baseDir(), err)
	}
	if err := writeAtomic(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// challengeServer answers Let's Encrypt's HTTP-01 fetches for as long as an
// order is in progress, and nothing else: every other path is a 404, so
// port 80 publishes nothing about the panel while it is open.
type challengeServer struct {
	mu      sync.Mutex
	answers map[string]string
}

func newChallengeServer() *challengeServer {
	return &challengeServer{answers: map[string]string{}}
}

func (c *challengeServer) set(path, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answers[path] = body
}

func (c *challengeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	body, ok := c.answers[r.URL.Path]
	c.mu.Unlock()
	if !ok || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, body)
}

// listen opens the challenge port for the length of one order. A port that
// is already taken is reported as exactly that, because the alternative is
// Let's Encrypt timing out against whatever does hold it and the operator
// reading a validation error about their DNS.
func (c *challengeServer) listen(ctx context.Context, addr string) (stop func(), err error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("port 80 is taken on this server, and Let's Encrypt proves a name by fetching a file from port 80, "+
			"so whatever holds it has to be stopped while the certificate is issued: %w", err)
	}
	srv := &http.Server{Handler: c, ReadHeaderTimeout: 10 * time.Second}
	go network.ServeHTTP(srv, ln, "certificate challenge")
	return func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}, nil
}
