package tunnel

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// The panel's own half of each tunnel: a Backhaul client dialling out to the
// front machine. Supervised like the other helpers rather than started and
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
	// fingerprint is everything the client process reads once, at startup. A
	// change to any of it has to restart the process; a change to anything
	// else must not, because a restart drops every customer riding the
	// tunnel at that moment.
	fingerprint string
}

// Manager holds the running clients.
type Manager struct {
	mu    sync.Mutex
	procs map[int]*managed
}

var (
	managerOnce sync.Once
	manager     *Manager
)

// GetManager returns the panel-wide manager.
func GetManager() *Manager {
	managerOnce.Do(func() { manager = &Manager{procs: map[int]*managed{}} })
	return manager
}

// fingerprint is what the client reads at startup. The token and the
// transport are in it because the two ends must agree on both, and the
// address and port because that is what it dials.
func fingerprint(p Pair) string {
	return fmt.Sprintf("%s|%d|%s|%s", p.FrontAddress, p.TunnelPort, p.Transport, p.Token)
}

// Ensure brings one tunnel's client to what the panel wants, and leaves a
// healthy one alone.
func (m *Manager) Ensure(p Pair) error {
	config, err := ClientConfig(p)
	if err != nil {
		return err
	}
	fp := fingerprint(p)

	m.mu.Lock()
	defer m.mu.Unlock()

	cur, had := m.procs[p.Id]
	running := had && cur.proc != nil && cur.proc.IsRunning()
	// Nothing the process reads has changed and it is alive: leave it be.
	// Restarting here would drop everybody on the tunnel for no reason, once
	// per reconcile, forever.
	if running && cur.fingerprint == fp {
		return nil
	}

	if had && cur.proc != nil {
		_ = cur.proc.Stop()
		delete(m.procs, p.Id)
	}

	if err := os.MkdirAll(runtimeDir(), 0o750); err != nil {
		return fmt.Errorf("make the tunnel's directory: %w", err)
	}
	// The token is in this file, so it is not readable by every account on
	// the host.
	if err := os.WriteFile(LocalConfigPath(p.Id), []byte(config), 0o600); err != nil {
		return fmt.Errorf("write the tunnel config: %w", err)
	}

	binary := BinaryPath()
	if binary == "" {
		return fmt.Errorf("backhaul is not installed on this panel; the tunnel cannot be dialled from here")
	}
	proc := sidecar.New(binary, fmt.Sprintf("tunnel %d", p.Id), "-c", LocalConfigPath(p.Id))
	if err := proc.Start(); err != nil {
		return fmt.Errorf("start the tunnel client: %w", err)
	}
	m.procs[p.Id] = &managed{proc: proc, fingerprint: fp}
	logger.Infof("tunnel: %d now dialling %s:%d", p.Id, p.FrontAddress, p.TunnelPort)
	return nil
}

// Remove stops one tunnel's client and forgets it.
func (m *Manager) Remove(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, had := m.procs[id]; had && cur.proc != nil {
		_ = cur.proc.Stop()
	}
	delete(m.procs, id)
	_ = os.Remove(LocalConfigPath(id))
}

// Reconcile moves the running set toward the wanted one, and stops whatever
// is no longer wanted. A tunnel removed in the panel must stop dialling, or
// the front keeps a live route to a panel that has disowned it.
func (m *Manager) Reconcile(wanted []Pair) {
	keep := make(map[int]bool, len(wanted))
	for _, p := range wanted {
		keep[p.Id] = true
		if err := m.Ensure(p); err != nil {
			logger.Warningf("tunnel %d: %v", p.Id, err)
		}
	}

	m.mu.Lock()
	running := make([]int, 0, len(m.procs))
	for id := range m.procs {
		if !keep[id] {
			running = append(running, id)
		}
	}
	m.mu.Unlock()

	for _, id := range running {
		m.Remove(id)
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
// and what it last said if it is not.
func (m *Manager) Status(id int) (running bool, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, had := m.procs[id]
	if !had || cur.proc == nil {
		return false, ""
	}
	return cur.proc.IsRunning(), cur.proc.Result()
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
