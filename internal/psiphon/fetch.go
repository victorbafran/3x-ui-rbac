package psiphon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

// binaryURL and wantSHA256 are variables so a test can serve its own build.
var (
	binaryURL  = "https://raw.githubusercontent.com/Psiphon-Labs/psiphon-tunnel-core-binaries/" + BinaryCommit + "/linux/psiphon-tunnel-core-x86_64"
	wantSHA256 = BinarySHA256
)

// maxBinarySize caps the download; the pinned build is about 36 MB.
const maxBinarySize = 128 << 20

var fetchMu sync.Mutex

// binaryPath is keyed by commit, so a panel that pins a newer build fetches it
// instead of running the old one.
func binaryPath() string {
	return filepath.Join(config.GetDBFolderPath(), "tools", "psiphon-"+BinaryCommit[:12], "x-ui-psiphon-linux-amd64")
}

// Installed reports whether the pinned build is already cached.
func Installed() bool {
	info, err := os.Stat(binaryPath())
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// Fetch returns the cached client, downloading the pinned build once. Nothing
// is kept unless its SHA-256 matches, so a file in place was checked.
func Fetch(ctx context.Context) (string, error) {
	fetchMu.Lock()
	defer fetchMu.Unlock()

	bin := binaryPath()
	if Installed() {
		return bin, nil
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		return "", fmt.Errorf("could not make %s: %w", filepath.Dir(bin), err)
	}
	tmp := bin + ".part"
	if err := download(ctx, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, bin); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("could not put %s in place: %w", bin, err)
	}
	return bin, nil
}

// download writes the build to dest and refuses it unless the checksum holds.
func download(ctx context.Context, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, binaryURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not download psiphon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("could not download psiphon: %s", resp.Status)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("could not write %s: %w", dest, err)
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(resp.Body, maxBinarySize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("could not download psiphon: %w", err)
	}
	if n > maxBinarySize {
		return fmt.Errorf("the psiphon download is larger than %d MB; refusing to install", maxBinarySize>>20)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != wantSHA256 {
		return fmt.Errorf("the psiphon download has SHA-256 %s, not the pinned %s; refusing to install", got, wantSHA256)
	}
	return nil
}
