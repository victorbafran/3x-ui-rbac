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
	"strings"
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
	// A candidate no client announced gets less: fresh clients for a country Psiphon
	// serves connected in 2-8 s on 2026-11-02, and the 23 candidates at 90 s each
	// added nine minutes to a scan without one connecting.
	scanCandidateBudget = 30 * time.Second
	scanMeasureBudget   = 30 * time.Second
	scanTick            = 250 * time.Millisecond
)

// ErrScanRunning refuses a second scan while one is still going.
var ErrScanRunning = errors.New("a Psiphon scan is already running")

// ScanResult is one country tried, with a client of its own or, OnRunning, through
// the one already carrying its customers; New is a country not offered before.
type ScanResult struct {
	Code      string
	Connected bool
	Exit      Exit
	Detail    string
	OnRunning bool
	New       bool
}

// ScanState is the last scan, kept until the next one starts; Results grow as
// countries finish, Total as clients announce countries not yet queued, and
// Stopped marks a scan cut short by shutdown or standby.
type ScanState struct {
	Running           bool
	Stopped           bool
	Started, Finished time.Time
	Total             int
	Results           []ScanResult
}

// ScanPlan is one scan: Codes to try, Running the enabled countries, measured through
// their own client while it runs, and Known the countries offered before it.
type ScanPlan struct {
	Codes   []string
	Running []Country
	Known   []string
	Taken   func(port int) bool
	// Finished gets the scan's end state, unless a shutdown or standby cut it short.
	Finished func(ScanState)
}

type scanner struct {
	mu     sync.Mutex
	state  ScanState
	held   map[int]bool
	cancel context.CancelFunc
	done   chan struct{}
	// The queue: codes waiting, every code ever queued, codes being tried now, and
	// wake, closed and replaced whenever any of them changes.
	pending []string
	queued  map[string]bool
	busy    int
	closed  bool
	wake    chan struct{}
	known   map[string]bool
	// quick are the candidates no client has announced, tried on the shorter budget.
	quick map[string]bool
}

// StartScan tries each country in the background: a running one through its own client,
// any other with a throwaway client, a few at a time, and the countries they announce too.
func (m *Manager) StartScan(plan ScanPlan) error {
	var list []string
	for _, code := range plan.Codes {
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
	running := map[string]int{}
	for _, c := range plan.Running {
		running[c.Code] = c.SocksPort
	}
	// The running ones go first: they need no client, so nothing they wait on.
	slices.SortStableFunc(list, func(a, b string) int {
		_, ra := running[a]
		_, rb := running[b]
		switch {
		case ra && !rb:
			return -1
		case rb && !ra:
			return 1
		}
		return 0
	})

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
	s.pending, s.queued, s.busy, s.closed, s.wake = list, map[string]bool{}, 0, false, make(chan struct{})
	for _, code := range list {
		s.queued[code] = true
	}
	s.known = map[string]bool{}
	for _, code := range plan.Known {
		s.known[code] = true
	}
	s.quick = unannounced(list, s.known, running)
	logger.Infof("psiphon: scanning %d countries, %d of them through their running client, %d new clients at a time", len(list), len(running), scanParallel)
	go m.runScan(ctx, bin, plan, running, done)
	return nil
}

func (m *Manager) runScan(ctx context.Context, bin string, plan ScanPlan, running map[string]int, done chan struct{}) {
	defer close(done)
	root := filepath.Join(Root(), "_scan")
	// Left by a panel that died mid-scan; only one scan runs, so none is in use.
	_ = os.RemoveAll(root)

	// Only the throwaway clients count against the three: a running one costs no memory.
	clients := make(chan struct{}, scanParallel)
	measuring := make(chan struct{}, testParallel)
	var wg sync.WaitGroup
	for {
		code, ok := m.scan.next(ctx)
		if !ok {
			break
		}
		if port, isRunning := running[code]; isRunning && m.Status(code).Running {
			select {
			case measuring <- struct{}{}:
			case <-ctx.Done():
				m.scan.finish()
				continue
			}
			wg.Go(func() {
				defer func() { <-measuring; m.scan.finish() }()
				connected := m.Status(code).Connected
				measureCtx, cancel := context.WithTimeout(ctx, scanMeasureBudget)
				exit := measureRunning(measureCtx, port)
				cancel()
				if ctx.Err() == nil {
					m.scan.record(ScanResult{Code: code, Connected: connected, Exit: exit, OnRunning: true})
				}
			})
			continue
		}
		select {
		case clients <- struct{}{}:
		case <-ctx.Done():
			m.scan.finish()
			continue
		}
		wg.Go(func() {
			defer func() { <-clients; m.scan.finish() }()
			r := scanClient(m, ctx, bin, root, code, plan.Taken)
			// A country cut off by a stop says nothing about the country.
			if ctx.Err() == nil {
				m.scan.record(r)
			}
		})
	}
	wg.Wait()
	_ = os.RemoveAll(root)

	s := &m.scan
	s.mu.Lock()
	final := s.state
	final.Running, final.Stopped, final.Finished = false, ctx.Err() != nil, time.Now()
	final.Results = slices.Clone(final.Results)
	s.mu.Unlock()
	worked := 0
	for _, r := range final.Results {
		if r.Exit.Works() {
			worked++
		}
	}
	logger.Infof("psiphon: scan done, %d of %d countries work", worked, final.Total)
	// The finds are saved before the scan shows as done, so a card that sees it done
	// also sees them offered; a second scan stays refused until then.
	if !final.Stopped && plan.Finished != nil {
		plan.Finished(final)
	}
	s.mu.Lock()
	s.state.Running, s.state.Stopped, s.state.Finished = false, final.Stopped, final.Finished
	s.mu.Unlock()
}

// next hands out the next country to try, waiting while the ones being tried may
// still announce more; false once none is left and none is being tried, or on a stop.
func (s *scanner) next(ctx context.Context) (string, bool) {
	for {
		s.mu.Lock()
		if ctx.Err() != nil || (len(s.pending) == 0 && s.busy == 0) {
			s.closed = true
			s.mu.Unlock()
			return "", false
		}
		if len(s.pending) > 0 {
			code := s.pending[0]
			s.pending = s.pending[1:]
			s.busy++
			s.mu.Unlock()
			return code, true
		}
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
		}
	}
}

func (s *scanner) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy--
	s.signal()
}

// heard queues every country a scan's client announced that the scan has not met,
// and counts it in the total, so the progress grows with what is left to try.
func (s *scanner) heard(codes []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	var fresh []string
	for _, code := range codes {
		// A candidate a client announced is a country Psiphon serves: full budget.
		delete(s.quick, code)
		if countryCode.MatchString(code) && !s.queued[code] {
			s.queued[code] = true
			fresh = append(fresh, code)
		}
	}
	if len(fresh) == 0 {
		return
	}
	s.pending = append(s.pending, fresh...)
	s.state.Total += len(fresh)
	s.signal()
	logger.Infof("psiphon: the scan heard of %s too", strings.Join(fresh, ", "))
}

// signal wakes next; the caller holds s.mu.
func (s *scanner) signal() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// unannounced are the candidates in codes that are neither on offer nor running.
func unannounced(codes []string, known map[string]bool, running map[string]int) map[string]bool {
	out := map[string]bool{}
	for _, code := range codes {
		if _, isRunning := running[code]; slices.Contains(CandidateRegions, code) && !known[code] && !isRunning {
			out[code] = true
		}
	}
	return out
}

// budget is how long a throwaway client for code may take to connect.
func (s *scanner) budget(code string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quick[code] {
		return scanCandidateBudget
	}
	return scanConnectBudget
}

func (s *scanner) record(r ScanResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.New = r.Connected && !s.known[r.Code]
	s.state.Results = append(s.state.Results, r)
}

// scanClient is a variable so a test in another package can answer for the clients.
var scanClient = (*Manager).scanOne

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

	live := &liveState{since: time.Now(), code: code, heard: m.scan.heard}
	proc := sidecar.New(bin, "psiphon scan "+code, "-config", path, "-dataRootDirectory", dir)
	proc.SetNoise(noticeNoise...)
	proc.OnLine(live.observe)
	if err := proc.Start(); err != nil {
		r.Detail = err.Error()
		return r
	}
	defer func() { _ = proc.Stop() }()

	r.Connected, r.Detail = waitConnected(ctx, proc, live, m.scan.budget(code))
	if r.Connected {
		measureCtx, cancel := context.WithTimeout(ctx, scanMeasureBudget)
		r.Exit = Measure(measureCtx, socksPort)
		cancel()
	}
	return r
}

// waitConnected is whether the client's tunnel came up within the budget, and
// if not, what the client said when it gave up.
func waitConnected(ctx context.Context, proc *sidecar.Process, live *liveState, limit time.Duration) (bool, string) {
	budget := time.NewTimer(limit)
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
