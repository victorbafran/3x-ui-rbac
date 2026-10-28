package tunnel

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// NewToken mints the shared secret that authenticates the two ends of a
// tunnel to each other.
//
// From crypto/rand, not the operator: a token short or memorable enough to
// type is one a stranger can guess, and the tunnel port is open to the whole
// internet. 32 bytes, URL-safe so it survives a TOML string and a shell
// argument without quoting surprises.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("no randomness for a tunnel token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// legToken is the token a second leg carries instead of the main one. Backhaul's
// udp transport sends its token readable, on its control connection and in the
// first datagram of every flow, and Backhaul's wss server lets in whoever shows
// the token. Were the UDP leg to carry the main token, a wss front would give
// away what its TLS hides. Derived rather than stored: both ends are built from
// the same Pair, so they agree with nothing new in the database.
func legToken(token, leg string) string {
	if token == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("x-ui tunnel leg " + leg))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
