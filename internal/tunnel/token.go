package tunnel

import (
	"crypto/rand"
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
