package psiphon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// Country is one enabled country and the loopback ports its client listens on.
type Country struct {
	Code      string
	SocksPort int
	HTTPPort  int
}

// Status is what the panel shows beside a country. Since is when the current
// process started, so a young one's silence is not read as a failure.
type Status struct {
	Running, Connected bool
	Region, Detail     string
	Since              time.Time
}

type managed struct {
	proc *sidecar.Process
	// fingerprint is the binary and the config bytes, all the client reads at
	// startup; anything else changing must not cut the customers riding it.
	fingerprint string
	live        *liveState
}

// liveState is read out of the client's notices under its own lock: the
// callback runs on the output pipe, which a Stop waits on.
type liveState struct {
	mu        sync.Mutex
	since     time.Time
	connected bool
	region    string
	code      string
	meter     *trafficMeter
}

func (s *liveState) observe(line string) {
	n, ok := ParseNotice(line)
	if !ok {
		return
	}
	if moved, ok := bytesTransferred(n); ok {
		// A scan's client has no meter: what it moves is the test, not customers.
		if s.meter != nil {
			s.meter.add(s.code, moved)
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if up, isTunnels := Connected(n); isTunnels {
		s.connected = up
	}
	if region := serverRegion(n); region != "" {
		s.region = region
	}
}

// Traffic is what a country's client carried: Down to customers, Up from them.
type Traffic struct{ Down, Up int64 }

// trafficMeter holds each country's bytes until they are flushed. It sits
// beside the clients, not in one, so a restart or a switch-off loses nothing.
type trafficMeter struct {
	mu      sync.Mutex
	pending map[string]Traffic
}

func (t *trafficMeter) add(code string, moved Traffic) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		t.pending = map[string]Traffic{}
	}
	sum := t.pending[code]
	t.pending[code] = Traffic{Down: sum.Down + moved.Down, Up: sum.Up + moved.Up}
}

func (t *trafficMeter) take() map[string]Traffic {
	t.mu.Lock()
	defer t.mu.Unlock()
	taken := t.pending
	t.pending = nil
	return taken
}

// forget drops what codes counted, every country's when codes is empty.
func (t *trafficMeter) forget(codes []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(codes) == 0 {
		t.pending = nil
		return
	}
	for _, code := range codes {
		delete(t.pending, code)
	}
}

// Manager holds one client per country.
type Manager struct {
	// reconcileMu keeps the job's pass and an operator's save from restarting
	// what the other just started; mu only guards the map, so Status never waits.
	reconcileMu sync.Mutex
	mu          sync.Mutex
	procs       map[string]*managed
	// flushMu keeps a reset from landing between a flush's take and its save;
	// the meter's own lock is held only briefly, as the clients' pipes wait on it.
	flushMu sync.Mutex
	traffic trafficMeter
	scan    scanner
}

var (
	managerOnce sync.Once
	manager     *Manager
)

// GetManager returns the panel-wide manager.
func GetManager() *Manager {
	managerOnce.Do(func() { manager = newManager() })
	return manager
}

func newManager() *Manager {
	return &Manager{procs: map[string]*managed{}}
}

// resolveBinary is a variable so a test can run a stand-in client.
var resolveBinary = func() (string, error) {
	if err := Supported(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return fetchPaced(ctx)
}

// Reconcile starts what is wanted, restarts a client whose binary or config
// changed or that died, and stops every other country.
func (m *Manager) Reconcile(wanted []Country) error {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()

	var errs []error
	keep := make(map[string]bool, len(wanted))
	var accepted []Country
	configs := make(map[string][]byte, len(wanted))
	ports := map[int]string{}
	for _, c := range wanted {
		if !countryCode.MatchString(c.Code) {
			errs = append(errs, fmt.Errorf("psiphon country %q is not a two-letter upper-case code", c.Code))
			continue
		}
		if keep[c.Code] {
			errs = append(errs, fmt.Errorf("psiphon %s is listed twice", c.Code))
			continue
		}
		// Kept even if its ports are refused below, so a bad edit does not
		// take down a client that is carrying customers.
		keep[c.Code] = true
		if other, taken := ports[c.SocksPort]; taken {
			errs = append(errs, fmt.Errorf("psiphon %s and %s both want port %d", other, c.Code, c.SocksPort))
			continue
		}
		if other, taken := ports[c.HTTPPort]; taken {
			errs = append(errs, fmt.Errorf("psiphon %s and %s both want port %d", other, c.Code, c.HTTPPort))
			continue
		}
		cfg, err := ConfigJSON(c.Code, c.SocksPort, c.HTTPPort)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ports[c.SocksPort], ports[c.HTTPPort] = c.Code, c.Code
		accepted = append(accepted, c)
		configs[c.Code] = cfg
	}

	if len(accepted) > 0 {
		bin, err := resolveBinary()
		if err != nil {
			errs = append(errs, fmt.Errorf("psiphon cannot run here: %w", err))
		} else {
			for _, c := range accepted {
				if err := m.ensure(bin, c, configs[c.Code]); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}

	m.drop(func(code string) bool { return !keep[code] })
	return errors.Join(errs...)
}

func (m *Manager) ensure(bin string, c Country, cfg []byte) error {
	fp := bin + "\n" + string(cfg)
	m.mu.Lock()
	cur := m.procs[c.Code]
	m.mu.Unlock()
	if cur != nil && cur.fingerprint == fp && cur.proc.IsRunning() {
		return nil
	}

	dir := filepath.Join(Root(), c.Code)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("psiphon %s: make %s: %w", c.Code, dir, err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		return fmt.Errorf("psiphon %s: write its config: %w", c.Code, err)
	}
	if cur != nil {
		_ = cur.proc.Stop()
	}

	live := &liveState{since: time.Now(), code: c.Code, meter: &m.traffic}
	proc := sidecar.New(bin, "psiphon "+c.Code, "-config", path, "-dataRootDirectory", dir)
	proc.SetNoise(noticeNoise...)
	proc.OnLine(live.observe)
	if err := proc.Start(); err != nil {
		m.mu.Lock()
		delete(m.procs, c.Code)
		m.mu.Unlock()
		return fmt.Errorf("psiphon %s: start: %w", c.Code, err)
	}
	m.mu.Lock()
	m.procs[c.Code] = &managed{proc: proc, fingerprint: fp, live: live}
	m.mu.Unlock()
	logger.Infof("psiphon: %s started, SOCKS on 127.0.0.1:%d", c.Code, c.SocksPort)
	return nil
}

// drop stops clients side by side, as a wedged one waits out its kill; their
// data directories stay, and the server list in them makes a restart quick.
func (m *Manager) drop(unwanted func(code string) bool) {
	m.mu.Lock()
	var stale []*managed
	var codes []string
	for code, cur := range m.procs {
		if unwanted(code) {
			stale = append(stale, cur)
			codes = append(codes, code)
			delete(m.procs, code)
		}
	}
	m.mu.Unlock()
	if len(stale) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, cur := range stale {
		wg.Go(func() { _ = cur.proc.Stop() })
	}
	wg.Wait()
	slices.Sort(codes)
	logger.Infof("psiphon: stopped %s", strings.Join(codes, ", "))
}

// Status is one country's client as the panel last saw it; a country with no
// client is the zero Status.
func (m *Manager) Status(code string) Status {
	m.mu.Lock()
	cur := m.procs[code]
	m.mu.Unlock()
	if cur == nil {
		return Status{}
	}
	running := cur.proc.IsRunning()
	cur.live.mu.Lock()
	s := Status{Running: running, Connected: running && cur.live.connected, Region: cur.live.region, Since: cur.live.since}
	cur.live.mu.Unlock()
	if !running {
		s.Detail = cur.proc.Result()
	}
	return s
}

// HasRunning reports whether any client is alive, so a job can skip its work
// on a panel that uses no country.
func (m *Manager) HasRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cur := range m.procs {
		if cur.proc.IsRunning() {
			return true
		}
	}
	return false
}

// FlushTraffic hands save what each country carried since the last flush; what
// save could not keep is counted again in the next one.
func (m *Manager) FlushTraffic(save func(map[string]Traffic) error) error {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()
	taken := m.traffic.take()
	if len(taken) == 0 {
		return nil
	}
	if err := save(taken); err != nil {
		for code, moved := range taken {
			m.traffic.add(code, moved)
		}
		return err
	}
	return nil
}

// ResetTraffic runs zero, then forgets what codes (every country when empty)
// counted since the last flush, so no flush can bring bytes from before it back.
func (m *Manager) ResetTraffic(codes []string, zero func() error) error {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()
	if err := zero(); err != nil {
		return err
	}
	m.traffic.forget(codes)
	return nil
}

// StopAll takes every client down, a scan's too, for a panel that is shutting
// down or going on standby.
func (m *Manager) StopAll() {
	m.StopScan()
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	m.drop(func(string) bool { return true })
}
