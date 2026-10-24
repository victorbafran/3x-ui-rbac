// Package certreload serves a TLS certificate from files that may be renewed in
// place, so a renewal reaches the next handshake instead of the next restart.
package certreload

import (
	"crypto/tls"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/util/filestamp"
)

// checkEvery is how often the files are looked at; a variable for tests.
var checkEvery = 30 * time.Second

// Loader holds the certificate last read from a pair of files.
type Loader struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	stamp   string
	checked time.Time
	now     func() time.Time
}

// New reads the pair once, failing exactly as tls.LoadX509KeyPair does.
func New(certFile, keyFile string) (*Loader, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	l := &Loader{certFile: certFile, keyFile: keyFile, cert: &cert, now: time.Now}
	l.stamp, l.checked = l.stampNow(), l.now()
	return l, nil
}

func (l *Loader) stampNow() string {
	return filestamp.Of(l.certFile) + "|" + filestamp.Of(l.keyFile)
}

// GetCertificate is tls.Config's hook. A pair that will not load, a renewal
// caught between its two renames for one, keeps the old certificate in service.
func (l *Loader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now := l.now(); now.Sub(l.checked) >= checkEvery {
		l.checked = now
		if stamp := l.stampNow(); stamp != l.stamp {
			if cert, err := tls.LoadX509KeyPair(l.certFile, l.keyFile); err == nil {
				l.cert, l.stamp = &cert, stamp
			}
		}
	}
	return l.cert, nil
}
