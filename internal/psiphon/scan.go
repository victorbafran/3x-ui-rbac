package psiphon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// A scan's clients borrow ports from here, past the countries' redirect ports
// (11581–11780), so nothing a country holds or will be given is ever taken.
const (
	scanPortFirst = 11781
	scanPortLast  = 11880
)

// scanParallel is three: a 458 MB box running six clients beside the panel was
// killed for memory.
const scanParallel = 3

// scanConnectBudget and scanMeasureBudget are variables so a test need not wait
// minutes for a country that never connects.
var (
	scanConnectBudget = 90 * time.Second
	scanMeasureBudget = 30 * time.Second
	scanTick          = 250 * time.Millisecond
)

// ErrScanRunning refuses a second scan while one is still going.
var ErrScanRunning = errors.New("a Psiphon scan is already running")

// ScanResult is one country tried with a client of its own: whether a tunnel
// came up, what it measured, and, when none did, what the client last said.
type ScanResult struct {
	Code      string
	Connected bool
	Exit      Exit
	Detail    string
}

// ScanState is the last scan, kept until the next one starts; Results grow as
// countries finish, and Stopped marks a scan cut short by shutdown or standby.
type ScanState struct {
	Running           bool
	Stopped           bool
	Started, Finished time.Time
	Total             int
	Results           []ScanResult
}

type scanner struct {
	mu     sync.Mutex
	state  ScanState
	held   map[int]bool
	cancel context.CancelFunc
	done   chan struct{}
}

// StartScan tries each country in the background, a few at a time, each with a
// throwaway client; taken names the ports something else of the panel holds.
func (m *Manager) StartScan(codes []string, taken func(port int) bool) error {
	var list []string
	for _, code := range codes {
		if !countryCode.MatchString(code) {
			return fmt.Errorf("psiphon country %q is not a two-letter upper-case code", code)
		}
		if !slices.Contains(list, code) {
			list = append(list, code)
		}
	}
	slices.Sort(list)
	if len(list) == 0 {
		return errors.New("no Psiphon country to scan")
	}

	bin, err := resolveBinary()
	if err != nil {
		return fmt.Errorf("psiphon cannot run here: %w", err)
	}
	s := &m.scan
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Running {
		return ErrScanRunning
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.state = ScanState{Running: true, Started: time.Now(), Total: len(list)}
	s.cancel, s.done = cancel, done
	logger.Infof("psiphon: scanning %d countries, %d at a time", len(list), scanParallel)
	go m.runScan(ctx, bin, list, taken, done)
	return nil
}

func (m *Manager) runScan(ctx context.Context, bin string, codes []string, taken func(int) bool, done chan struct{}) {
	defer close(done)
	root := filepath.Join(Root(), "_scan")
	// Left by a panel that died mid-scan; only one scan runs, so none is in use.
	_ = os.RemoveAll(root)

	queue := make(chan string)
	var wg sync.WaitGroup
	for range min(scanParallel, len(codes)) {
		wg.Go(func() {
			for code := range queue {
				r := m.scanOne(ctx, bin, root, code, taken)
				// A country cut off by a stop says nothing about the country.
				if ctx.Err() == nil {
					m.scan.record(r)
				}
			}
		})
	}
feed:
	for _, code := range codes {
		select {
		case queue <- code:
		case <-ctx.Done():
			break feed
		}
	}
	close(queue)
	wg.Wait()
	_ = os.RemoveAll(root)

	s := &m.scan
	s.mu.Lock()
	s.state.Running, s.state.Stopped, s.state.Finished = false, ctx.Err() != nil, time.Now()
	worked := 0
	for _, r := range s.state.Results {
		if r.Exit.Works() {
			worked++
		}
	}
	s.mu.Unlock()
	logger.Infof("psiphon: scan done, %d of %d countries work", worked, len(codes))
}

func (s *scanner) record(r ScanResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Results = append(s.state.Results, r)
}

// scanOne runs one throwaway client in a fresh directory, measures it if it
// connects, and leaves neither the process nor the directory behind.
func (m *Manager) scanOne(ctx context.Context, bin, root, code string, taken func(int) bool) ScanResult {
	r := ScanResult{Code: code}
	socksPort, httpPort, err := m.scan.borrow(taken)
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	defer m.scan.giveBack(socksPort, httpPort)
	if err := os.MkdirAll(root, 0o750); err != nil {
		r.Detail = err.Error()
		return r
	}
	dir, err := os.MkdirTemp(root, code+"-")
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	defer func() { _ = os.RemoveAll(dir) }()
	cfg, err := ConfigJSON(code, socksPort, httpPort)
	if err != nil {
		r.Detail = err.Error()
		return r
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		r.Detail = err.Error()
		return r
	}

	live := &liveState{since: time.Now(), code: code}
	proc := sidecar.New(bin, "psiphon scan "+code, "-config", path, "-dataRootDirectory", dir)
	proc.SetNoise(noticeNoise...)
	proc.OnLine(live.observe)
	if err := proc.Start(); err != nil {
		r.Detail = err.Error()
		return r
	}
	defer func() { _ = proc.Stop() }()

	r.Connected, r.Detail = waitConnected(ctx, proc, live)
	if r.Connected {
		measureCtx, cancel := context.WithTimeout(ctx, scanMeasureBudget)
		r.Exit = Measure(measureCtx, socksPort)
		cancel()
	}
	return r
}

// waitConnected is whether the client's tunnel came up within the budget, and
// if not, what the client said when it gave up.
func waitConnected(ctx context.Context, proc *sidecar.Process, live *liveState) (bool, string) {
	budget := time.NewTimer(scanConnectBudget)
	defer budget.Stop()
	tick := time.NewTicker(scanTick)
	defer tick.Stop()
	for {
		running := proc.IsRunning()
		live.mu.Lock()
		up := live.connected
		live.mu.Unlock()
		if up && running {
			return true, ""
		}
		if !running {
			return false, proc.Result()
		}
		select {
		case <-ctx.Done():
			return false, ""
		case <-budget.C:
			return false, ""
		case <-tick.C:
		}
	}
}

// borrow hands out two ports in the scan's range that no scan client, nothing
// of the panel, and nothing else on the box is listening on.
func (s *scanner) borrow(taken func(int) bool) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held == nil {
		s.held = map[int]bool{}
	}
	var got []int
	for port := scanPortFirst; port <= scanPortLast && len(got) < 2; port++ {
		if s.held[port] || (taken != nil && taken(port)) || !loopbackFree(port) {
			continue
		}
		got = append(got, port)
	}
	if len(got) < 2 {
		return 0, 0, fmt.Errorf("no two free ports are left for a scan between %d and %d", scanPortFirst, scanPortLast)
	}
	s.held[got[0]], s.held[got[1]] = true, true
	return got[0], got[1], nil
}

func (s *scanner) giveBack(ports ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, port := range ports {
		delete(s.held, port)
	}
}

func loopbackFree(port int) bool {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// ScanHolds reports whether a scan client is using port right now, so an inbound
// saved meanwhile is refused rather than left unable to bind.
func (m *Manager) ScanHolds(port int) bool {
	m.scan.mu.Lock()
	defer m.scan.mu.Unlock()
	return m.scan.held[port]
}

// ScanState is the running or last scan, a copy the caller may keep.
func (m *Manager) ScanState() ScanState {
	m.scan.mu.Lock()
	defer m.scan.mu.Unlock()
	st := m.scan.state
	st.Results = slices.Clone(st.Results)
	return st
}

// StopScan cuts a running scan short and waits until its clients are stopped
// and their directories gone.
func (m *Manager) StopScan() {
	m.scan.mu.Lock()
	cancel, done := m.scan.cancel, m.scan.done
	m.scan.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}
