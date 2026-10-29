package tunnel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// serveBackhaul answers Backhaul's release URLs with archive under the checksum listed for it,
// and points the fetch at it with an empty tools folder.
func serveBackhaul(t *testing.T, archive []byte, listed []byte) *atomic.Int32 {
	t.Helper()
	sum := sha256.Sum256(listed)
	file := "backhaul_linux_" + panelArch() + ".tar.gz"
	sums := fmt.Sprintf("%s  backhaul_darwin_amd64.tar.gz\n%s  %s\n", strings.Repeat("0", 64), hex.EncodeToString(sum[:]), file)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + Release + "/checksums.txt":
			_, _ = w.Write([]byte(sums))
		case "/" + Release + "/" + file:
			hits.Add(1)
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	saved := backhaulReleaseBase
	t.Cleanup(func() { backhaulReleaseBase = saved })
	backhaulReleaseBase = srv.URL + "/"
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	return &hits
}

// A restored or promoted panel has the Backhaul fronts but not the binary someone once put in
// /usr/local/bin by hand: it fetches the pinned release, once, and only one matching its checksum.
func TestAPanelWithoutBackhaulFetchesThePinnedRelease(t *testing.T) {
	original := binaryCandidates
	binaryCandidates = []string{filepath.Join(t.TempDir(), "definitely-not-here")}
	t.Cleanup(func() { binaryCandidates = original })

	archive := tarGz(t, map[string][]byte{"backhaul": []byte("the binary")})
	hits := serveBackhaul(t, archive, archive)
	for range 2 {
		bin, err := clientBinary(goodPair())
		if err != nil {
			t.Fatalf("a genuine release was refused: %v", err)
		}
		want := filepath.Join(os.Getenv("XUI_DB_FOLDER"), "tools", "backhaul-"+Release, "backhaul-linux-"+panelArch())
		if data, _ := os.ReadFile(bin); bin != want || string(data) != "the binary" {
			t.Fatalf("got %s holding %q, want %s", bin, data, want)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("two starts downloaded %d times, want once", hits.Load())
	}

	tampered := tarGz(t, map[string][]byte{"backhaul": []byte("something else")})
	serveBackhaul(t, tampered, archive)
	if _, err := FetchBackhaul(t.Context(), panelArch()); err == nil || !strings.Contains(err.Error(), "does not match its published checksum") {
		t.Fatalf("an archive that does not match its checksum got through: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(os.Getenv("XUI_DB_FOLDER"), "tools", "backhaul-"+Release)); len(entries) != 0 {
		t.Fatalf("a refused download left %d files behind", len(entries))
	}
}
