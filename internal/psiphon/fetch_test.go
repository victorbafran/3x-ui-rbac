package psiphon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serveBuild(t *testing.T, build []byte, want string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(build)
	}))
	t.Cleanup(srv.Close)
	savedURL, savedSum := binaryURL, wantSHA256
	t.Cleanup(func() { binaryURL, wantSHA256 = savedURL, savedSum })
	binaryURL, wantSHA256 = srv.URL+"/psiphon-tunnel-core-x86_64", want
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	return &hits
}

func sumOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// The pinned checksum is the only integrity check Psiphon offers, so a build
// that does not match is never kept, not even as a .part.
func TestFetchKeepsOnlyTheBuildWithThePinnedChecksum(t *testing.T) {
	serveBuild(t, []byte("a tampered build"), sumOf([]byte("the pinned build")))
	if _, err := Fetch(t.Context()); err == nil || !strings.Contains(err.Error(), "refusing to install") {
		t.Fatalf("a build with the wrong checksum got through: %v", err)
	}
	if Installed() {
		t.Fatal("a refused build counts as installed")
	}
	if entries, _ := os.ReadDir(filepath.Dir(binaryPath())); len(entries) != 0 {
		t.Fatalf("a refused download left %d files behind", len(entries))
	}
}

func TestFetchCachesTheBuildPerCommit(t *testing.T) {
	build := []byte("the pinned build")
	hits := serveBuild(t, build, sumOf(build))
	bin, err := Fetch(t.Context())
	if err != nil {
		t.Fatalf("the pinned build was refused: %v", err)
	}
	if want := filepath.Join(os.Getenv("XUI_DB_FOLDER"), "tools", "psiphon-196a723c3f07", "x-ui-psiphon-linux-amd64"); bin != want {
		t.Fatalf("cached at %s, want %s", bin, want)
	}
	if data, _ := os.ReadFile(bin); string(data) != string(build) {
		t.Fatalf("cached %q", data)
	}
	if again, err := Fetch(t.Context()); err != nil || again != bin || hits.Load() != 1 {
		t.Fatalf("second Fetch = %s, %v after %d downloads; want the cache and one download", again, err, hits.Load())
	}
}

// The job asks for the client every 30 s, and each try may pull the whole build: one that
// failed waits ten minutes, but a build installed meanwhile is taken at once.
func TestAFailedFetchIsNotTriedAgainOnEveryTick(t *testing.T) {
	served := []byte("the build being served")
	hits := serveBuild(t, served, sumOf([]byte("the pinned build")))
	t.Cleanup(func() { fetchPause.until, fetchPause.err = time.Time{}, nil })
	for i := range 3 {
		_, err := fetchPaced(t.Context())
		if err == nil || !strings.Contains(err.Error(), "refusing to install") || errors.Is(err, ErrFetchWaiting) != (i > 0) {
			t.Fatalf("try %d: %v", i+1, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("three ticks downloaded %d times, want once", hits.Load())
	}

	fetchPause.until = time.Now()
	if _, err := fetchPaced(t.Context()); err == nil || errors.Is(err, ErrFetchWaiting) || hits.Load() != 2 {
		t.Fatalf("once the wait is over: %v after %d downloads, want a second try", err, hits.Load())
	}

	wantSHA256 = sumOf(served)
	if _, err := Fetch(t.Context()); err != nil {
		t.Fatalf("an install during the wait: %v", err)
	}
	if bin, err := fetchPaced(t.Context()); err != nil || bin != binaryPath() || hits.Load() != 3 {
		t.Fatalf("after the install the tick got %q, %v after %d downloads; want the installed build", bin, err, hits.Load())
	}
}
