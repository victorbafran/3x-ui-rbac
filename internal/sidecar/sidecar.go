// Package sidecar supervises a long-running helper process that serves one
// inbound: start it, know whether it is alive, stop it without leaving it
// behind, and keep the last thing it said so the panel can show the operator
// why it is not running.
//
// internal/ocserv keeps its own Process, written first, and shares only the
// LineWriter; moving the rest is a separate change, as that protocol is in use.
package sidecar

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

var (
	gracefulStopTimeout = 5 * time.Second
	forceStopTimeout    = 2 * time.Second
)

// Process is one supervised helper.
type Process struct {
	mu              sync.RWMutex
	cmd             *exec.Cmd
	done            chan struct{}
	binary          string
	args            []string
	log             *LineWriter
	exitErr         error
	intentionalStop atomic.Bool
}

// New describes a process without starting it. label prefixes every line the
// child writes into the panel log.
func New(binary, label string, args ...string) *Process {
	return &Process{binary: binary, args: args, log: NewLineWriter(label)}
}

// SetNoise folds the lines the child prints for every stranger who knocks;
// call it before Start.
func (p *Process) SetNoise(rules ...NoiseRule) {
	p.log.setNoise(rules)
}

// OnLine hands fn every non-blank line the child prints, noise included, so
// a caller can read state out of it; call it before Start.
func (p *Process) OnLine(fn func(line string)) {
	p.log.setOnLine(fn)
}

// IsRunning reports whether the child is alive.
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

// Result is the last thing the child said, or why it exited. This is what the
// panel shows beside an inbound that is not running.
func (p *Process) Result() string {
	if line := p.log.LastLine(); line != "" {
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

// Start launches the child in the foreground, so the panel owns its lifetime
// and a panel that dies does not leave a server listening behind it.
func (p *Process) Start() error {
	if p.IsRunning() {
		return errors.New("already running")
	}
	if strings.TrimSpace(p.binary) == "" {
		return errors.New("no binary to run")
	}
	cmd := exec.CommandContext(context.Background(), p.binary, p.args...)
	cmd.Stdout = p.log
	cmd.Stderr = p.log
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
	p.log.Flush()
	// A process the panel stopped on purpose exits non-zero and that is not
	// news. Only an exit nobody asked for is worth an error in the log.
	if err == nil || p.intentionalStop.Load() {
		return
	}
	logger.Errorf("%s exited: %v", p.log.label, err)
	p.mu.Lock()
	p.exitErr = err
	p.mu.Unlock()
}

// Exited waits up to d for a child asked to go some other way, through its own
// management interface for one, and reports whether it went.
func (p *Process) Exited(d time.Duration) bool {
	p.intentionalStop.Store(true)
	p.mu.RLock()
	done := p.done
	p.mu.RUnlock()
	return waitForExit(done, d) == nil
}

// Stop asks the child to go, and kills it if it will not.
//
// Both halves matter. Without the ask, a server is cut off mid-session and
// every client waits out its own timeout; without the kill, one wedged
// process holds its port and the inbound never comes back.
func (p *Process) Stop() error {
	if !p.IsRunning() {
		return errors.New("not running")
	}
	p.intentionalStop.Store(true)
	p.mu.RLock()
	cmd, done := p.cmd, p.done
	p.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return errors.New("not running")
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

	logger.Warningf("%s did not stop gracefully, killing it", p.log.label)
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
		return errors.New("timed out waiting for the process to exit")
	}
}
