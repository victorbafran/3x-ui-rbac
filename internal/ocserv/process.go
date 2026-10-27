package ocserv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

var (
	gracefulStopTimeout = 5 * time.Second
	forceStopTimeout    = 2 * time.Second
)

// probeNoise is what scanners' half-finished handshakes leave, ~30K lines a day.
// Timeouts, fatal alerts and bad certificates stay: they diagnose DPI and pins.
var probeNoise = []sidecar.NoiseRule{
	handshakeNoise("non-properly terminated", "The TLS connection was non-properly terminated"),
	handshakeNoise("error decoding", "Error decoding the received TLS packet"),
	handshakeNoise("unsupported version", "A packet with illegal or unsupported version was received"),
	handshakeNoise("unexpected packet", "An unexpected TLS packet was received"),
	handshakeNoise("no cipher suites", "No supported cipher suites have been found"),
	handshakeNoise("pull function", "Error in the pull function"),
	{Kind: "unknown record type", Line: regexp.MustCompile(`^ocserv\[\d+\]: worker: \S+ warning: Received record packet of unknown type \d+$`)},
}

// handshakeNoise matches only a worker that never got a session: one that had
// one names it as worker[id], and its errors are a customer's.
func handshakeNoise(kind, message string) sidecar.NoiseRule {
	return sidecar.NoiseRule{
		Kind: kind,
		Line: regexp.MustCompile(`^ocserv\[\d+\]: GnuTLS error \(at worker-vpn\.c:\d+\): ` + regexp.QuoteMeta(message) + `\.$`),
	}
}

// Process wraps one ocserv invocation, serving one cisco inbound.
type Process struct {
	mu              sync.RWMutex
	cmd             *exec.Cmd
	done            chan struct{}
	configPath      string
	logWriter       *sidecar.LineWriter
	exitErr         error
	intentionalStop atomic.Bool
}

func newProcess(configPath, label string) *Process {
	return &Process{configPath: configPath, logWriter: sidecar.NewLineWriter("ocserv: "+label, probeNoise...)}
}

// IsRunning reports whether the ocserv process is currently running.
func (p *Process) IsRunning() bool {
	p.mu.RLock()
	cmd, done := p.cmd, p.done
	p.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return false
	}
	if done != nil {
		select {
		case <-done:
			return false
		default:
		}
	}
	return true
}

// GetResult returns the last log line, or the exit error.
func (p *Process) GetResult() string {
	if line := p.logWriter.LastLine(); line != "" {
		return line
	}
	p.mu.RLock()
	exitErr := p.exitErr
	p.mu.RUnlock()
	if exitErr != nil {
		return exitErr.Error()
	}
	return ""
}

// Start launches ocserv in the foreground against its generated config.
func (p *Process) Start() error {
	if p.IsRunning() {
		return errors.New("ocserv is already running")
	}
	bin := ServerBinaryPath()
	if bin == "" {
		return errors.New("ocserv is not installed")
	}
	// -f keeps ocserv in the foreground so the panel owns its lifetime, and
	// -d 1 is the lowest level that still names why a handshake failed.
	cmd := exec.CommandContext(context.Background(), bin, "-f", "-d", "1", "-c", p.configPath)
	cmd.Stdout = p.logWriter
	cmd.Stderr = p.logWriter
	done := make(chan struct{})
	p.mu.Lock()
	p.cmd = cmd
	p.done = done
	p.exitErr = nil
	p.mu.Unlock()
	p.intentionalStop.Store(false)
	if err := cmd.Start(); err != nil {
		close(done)
		p.mu.Lock()
		p.cmd = nil
		p.mu.Unlock()
		return err
	}
	go p.wait(cmd, done)
	return nil
}

func (p *Process) wait(cmd *exec.Cmd, done chan struct{}) {
	defer close(done)
	err := cmd.Wait()
	p.logWriter.Flush()
	if err == nil || p.intentionalStop.Load() {
		return
	}
	logger.Errorf("ocserv: process exited: %v", err)
	p.mu.Lock()
	p.exitErr = err
	p.mu.Unlock()
}

// Stop terminates ocserv gracefully, falling back to a kill. ocserv forks a
// worker per session; terminating the main process takes the workers with it.
func (p *Process) Stop() error {
	if !p.IsRunning() {
		return errors.New("ocserv is not running")
	}
	p.intentionalStop.Store(true)
	p.mu.RLock()
	cmd, done := p.cmd, p.done
	p.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return errors.New("ocserv is not running")
	}

	if err := terminate(cmd.Process); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return waitForExit(done, forceStopTimeout)
		}
		return err
	}
	if err := waitForExit(done, gracefulStopTimeout); err == nil {
		return nil
	}

	logger.Warning("ocserv: process did not stop gracefully, killing it")
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return waitForExit(done, forceStopTimeout)
}

func waitForExit(done <-chan struct{}, timeout time.Duration) error {
	if done == nil {
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return fmt.Errorf("timed out waiting for ocserv to stop after %s", timeout)
	}
}
