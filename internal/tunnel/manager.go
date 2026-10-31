package tunnel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// The panel's own half of each tunnel: a client dialling out to the front
// machine, Backhaul's or BackPack's, one per leg. Supervised like the other helpers rather than started and
// forgotten — a tunnel whose client died is a front machine still accepting
// customers and dropping every one of them, and nothing about the panel
// looks wrong while it happens.

// runtimeDirOverride lets a test write somewhere harmless.
var runtimeDirOverride string

func runtimeDir() string {
	if runtimeDirOverride != "" {
		return runtimeDirOverride
	}
	return filepath.Join(config.GetDBFolderPath(), "tunnel")
}

// LocalConfigPath is where the panel keeps one tunnel's client config. Named
// after the tunnel for the same reason the remote one is: two tunnels sharing
// a path means standing up the second silently retargets the first.
func LocalConfigPath(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("client-%d.toml", id))
}

type managed struct {
	proc *sidecar.Process
	// configPath is the file this leg's client reads, removed with it.
	configPath string
	// fingerprint is everything the client process reads once, at startup. A
	// change to any of it has to restart the process; a change to anything
	// else must not, because a restart drops every customer riding the
	// tunnel at that moment.
	fingerprint string
}

// Manager holds the running clients.
type Manager struct {
	mu    sync.Mutex
	procs map[string]*managed // by Pair.Key: one entry per leg
}

var (
	managerOnce sync.Once
	manager     *Manager
)

// GetManager returns the panel-wide manager.
func GetManager() *Manager {
	managerOnce.Do(func() { manager = &Manager{procs: map[string]*managed{}} })
	return manager
}

// fingerprint is what the client reads at startup. The token and the
// transport are in it because the two ends must agree on both, the address
// and port because that is what it dials, and the tool because it decides
// which binary runs and what the config even means.
func fingerprint(p Pair) string {
	return fmt.Sprintf("%s|%s|%d|%s|%s", p.Tool.norm(), p.FrontAddress, p.TunnelPort, p.Transport, p.Token)
}

// clientBinary runs this leg's client here: BackPack's own verified copy, never the operator's;
// Backhaul's from this host, else the pinned release, as a restored or promoted panel has none.
func clientBinary(p Pair) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if p.Tool.norm() == ToolBackPack {
		bin, err := FetchBackPack(ctx, panelArch())
		if err != nil {
			return "", fmt.Errorf("backpack is not installed on this panel and could not be fetched: %w", err)
		}
		return bin, nil
	}
	if bin := BinaryPath(); bin != "" {
		return bin, nil
	}
	bin, err := FetchBackhaul(ctx, panelArch())
	if err != nil {
		return "", fmt.Errorf("backhaul is not installed on this panel and could not be fetched: %w", err)
	}
	return bin, nil
}

// panelArch names this machine's architecture the way BackPack's releases do.
func panelArch() string {
	if runtime.GOARCH == "arm" {
		return "armv7"
	}
	return runtime.GOARCH
}

// Ensure brings one leg's client to what the panel wants, and leaves a healthy
// one alone.
func (m *Manager) Ensure(p Pair) error {
	config, err := ClientConfig(p)
	if err != nil {
		return err
	}
	fp := fingerprint(p)
	key := p.Key()

	m.mu.Lock()
	cur, had := m.procs[key]
	running := had && cur.proc != nil && cur.proc.IsRunning()
	m.mu.Unlock()
	// Nothing the process reads has changed and it is alive: leave it be.
	// Restarting here would drop everybody on the tunnel for no reason, once
	// per reconcile, forever.
	if running && cur.fingerprint == fp {
		return nil
	}

	// Resolved before the lock is taken again: the first BackPack fetch is a
	// download, and the page reading tunnel status must not wait on it.
	binary, err := clientBinary(p)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, had := m.procs[key]; had && cur.proc != nil {
		_ = cur.proc.Stop()
		delete(m.procs, key)
	}

	if err := os.MkdirAll(runtimeDir(), 0o750); err != nil {
		return fmt.Errorf("make the tunnel's directory: %w", err)
	}
	// The token is in this file, so it is not readable by every account on
	// the host.
	path := p.localConfigPath()
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		return fmt.Errorf("write the tunnel config: %w", err)
	}

	proc := sidecar.New(binary, "tunnel "+key, "-c", path)
	proc.SetNoise(acmePortNoise...)
	if err := proc.Start(); err != nil {
		return fmt.Errorf("start the tunnel client: %w", err)
	}
	m.procs[key] = &managed{proc: proc, configPath: path, fingerprint: fp}
	logger.Infof("tunnel: %s now dialling %s:%d over %s", key, p.FrontAddress, p.TunnelPort, p.Tool.norm())
	return nil
}

// acmePortNoise folds BackPack's six-line refusal for a scanner reaching
// ACMEPort between issuances; the same refusal for any other port is a real fault.
var acmePortNoise = []sidecar.NoiseRule{{
	Kind: fmt.Sprintf("nothing on :%d", ACMEPort),
	Line: regexp.MustCompile(`\[ERROR\] nothing is listening on 127\.0\.0\.1:` + strconv.Itoa(ACMEPort) + ` on THIS server\.$`),
	Continuation: regexp.MustCompile(`^(The tunnel delivered the connection correctly|service being forwarded to\. Either it is not running|` +
		`to a different address \(many panels bind a public IP|Check with:\s+ss -tlnp \| grep :?` + strconv.Itoa(ACMEPort) + `$|` +
		`\(repeats are suppressed for \d+s\)$)`),
}}

// ownedBy is whether a leg's key belongs to tunnel id.
func ownedBy(key string, id int) bool {
	s := strconv.Itoa(id)
	return key == s || strings.HasPrefix(key, s+"-")
}

// Remove stops every leg of one tunnel's client and forgets them.
func (m *Manager) Remove(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, cur := range m.procs {
		if !ownedBy(key, id) {
			continue
		}
		if cur.proc != nil {
			_ = cur.proc.Stop()
		}
		if cur.configPath != "" {
			_ = os.Remove(cur.configPath)
		}
		delete(m.procs, key)
	}
	_ = os.Remove(LocalConfigPath(id))
}

// Reconcile moves the running set toward the wanted one, and stops whatever
// is no longer wanted. A tunnel removed in the panel must stop dialling, or
// the front keeps a live route to a panel that has disowned it. The wanted
// list holds every leg of every tunnel.
func (m *Manager) Reconcile(wanted []Pair) {
	keep := make(map[string]bool, len(wanted))
	for _, p := range wanted {
		keep[p.Key()] = true
		if err := m.Ensure(p); err != nil {
			logger.Warningf("tunnel %s: %v", p.Key(), err)
		}
	}

	m.drop(func(key string) bool { return !keep[key] })
}

// Retain stops tunnel id's legs that are not in keep: a leg the front no
// longer carries would otherwise dial a port nothing listens on, for ever.
func (m *Manager) Retain(id int, keep []Pair) {
	want := make(map[string]bool, len(keep))
	for _, p := range keep {
		want[p.Key()] = true
	}
	m.drop(func(key string) bool { return ownedBy(key, id) && !want[key] })
}

func (m *Manager) drop(unwanted func(key string) bool) {
	m.mu.Lock()
	var stale []*managed
	for key, cur := range m.procs {
		if unwanted(key) {
			stale = append(stale, cur)
			delete(m.procs, key)
		}
	}
	m.mu.Unlock()

	for _, cur := range stale {
		if cur.proc != nil {
			_ = cur.proc.Stop()
		}
		if cur.configPath != "" {
			_ = os.Remove(cur.configPath)
		}
	}
}

// HasRunning reports whether any tunnel client is alive, so a job can skip
// its work entirely on a panel that uses no tunnels.
func (m *Manager) HasRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cur := range m.procs {
		if cur.proc != nil && cur.proc.IsRunning() {
			return true
		}
	}
	return false
}

// Status is what the panel shows beside a tunnel: whether its client is up,
// and what it last said if it is not. A tunnel is up only when every leg is:
// a Backhaul front whose UDP leg died still carries TCP and silently drops
// every WireGuard and Hysteria customer.
func (m *Manager) Status(id int) (running bool, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := false
	running = true
	for key, cur := range m.procs {
		if !ownedBy(key, id) || cur.proc == nil {
			continue
		}
		seen = true
		if !cur.proc.IsRunning() {
			running = false
			if detail == "" {
				detail = cur.proc.Result()
			}
		}
	}
	if !seen {
		return false, ""
	}
	return running, detail
}

// binaryCandidates are where Backhaul lands. The panel installs it on the
// front machine itself, but its own copy comes from the operator or from the
// same install script run here, so both the usual places are tried.
var binaryCandidates = []string{"/usr/local/bin/backhaul", "/usr/bin/backhaul"}

// BinaryPath finds the Backhaul binary on this host, or returns empty. Empty
// is reported to the operator as a missing binary rather than as a tunnel
// that will not come up, which is the same distinction the cisco and OpenVPN
// inbounds make.
func BinaryPath() string {
	for _, path := range binaryCandidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}
