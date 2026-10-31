package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

// backhaulReleaseBase is where Backhaul's releases are fetched from; a test points it at a
// local server.
var backhaulReleaseBase = "https://github.com/Musixal/Backhaul/releases/download/"

var backhaulFetchMu sync.Mutex

// FetchBackhaul returns the panel's own copy of the pinned Backhaul for arch, fetched once and
// checked against the release's checksum list as the front's install checks it.
func FetchBackhaul(ctx context.Context, arch string) (string, error) {
	if arch != "amd64" && arch != "arm64" {
		return "", fmt.Errorf("backhaul publishes no build for %s", arch)
	}
	backhaulFetchMu.Lock()
	defer backhaulFetchMu.Unlock()

	dir := filepath.Join(config.GetDBFolderPath(), "tools", "backhaul-"+Release)
	bin := filepath.Join(dir, "backhaul-linux-"+arch)
	if info, err := os.Stat(bin); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		return bin, nil
	}
	base := backhaulReleaseBase + Release + "/"
	sums, err := download(ctx, base+"checksums.txt", 1<<20)
	if err != nil {
		return "", err
	}
	file := "backhaul_linux_" + arch + ".tar.gz"
	want, err := sumFor(sums, file)
	if err != nil {
		return "", err
	}
	tgz, err := download(ctx, base+file, 128<<20)
	if err != nil {
		return "", err
	}
	if got := sha256.Sum256(tgz); hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("the downloaded %s does not match its published checksum; refusing to install", file)
	}
	data, err := extractBinary(tgz, "backhaul")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("could not make %s: %w", dir, err)
	}
	tmp := bin + ".part"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("could not write %s: %w", bin, err)
	}
	if err := os.Rename(tmp, bin); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("could not put %s in place: %w", bin, err)
	}
	return bin, nil
}
