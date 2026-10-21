// Package filestamp names a file's content in a few characters, so a restart
// fingerprint can tell a file replaced in place from one left alone.
package filestamp

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
)

// Of returns a short digest of the file at path, "" for no path, and
// "missing" for a path that cannot be read. A sidecar that loads its
// certificate once at startup needs exactly this: the path never moves when
// the certificate is renewed, so a fingerprint of the path alone kept the old
// certificate in service until something else happened to restart it.
func Of(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "missing"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
