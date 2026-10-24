// Package certreload serves a TLS certificate from files that may be renewed in
// place, so a renewal reaches the next handshake instead of the next restart.
package certreload

import (
	"crypto/tls"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/filestamp"
)

// checkEvery is how often the files are looked at; a variable for tests.
var checkEvery = 30 * time.Second

// loadPair is tls.LoadX509KeyPair; a variable so a test can renew mid-load.
var loadPair = tls.LoadX509KeyPair

// Loader holds the certificate last read from a pair of files.
type Loader struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	stamp   string
	failed  string
	checked time.Time
	now     func() time.Time
}

// New reads the pair once, failing exactly as tls.LoadX509KeyPair does. The
// stamp is taken first, so a renewal landing mid-load is read at the next check.
func New(certFile, keyFile string) (*Loader, error) {
	l := &Loader{certFile: certFile, keyFile: keyFile, now: time.Now}
	stamp := l.stampNow()
	cert, err := loadPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	l.cert, l.stamp, l.checked = &cert, stamp, l.now()
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
			cert, err := loadPair(l.certFile, l.keyFile)
			switch {
			case err == nil:
				l.cert, l.stamp, l.failed = &cert, stamp, ""
			case stamp != l.failed:
				// Said once per content, not at every check while it stays broken.
				l.failed = stamp
				logger.Warningf("certificate %s changed but will not load; still serving the previous one: %v", l.certFile, err)
			}
		}
	}
	return l.cert, nil
}
