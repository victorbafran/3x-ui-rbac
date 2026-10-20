// Package sidecar supervises a long-running helper process that serves one
// inbound: start it, know whether it is alive, stop it without leaving it
// behind, and keep the last thing it said so the panel can show the operator
// why it is not running.
//
// internal/ocserv has its own copy of this, written first. This package is
// the shared one; moving ocserv onto it is a separate change, because that
// protocol is in use and its tests need a linux host to run.
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

// logWriter forwards the child's output into the panel log line by line, and
// remembers the last line.
//
// The last line is what the panel shows for an instance that will not start.
// Without it the operator sees an inbound that is simply absent, which is the
// hardest kind of failure to act on: the reason was printed and thrown away.
type logWriter struct {
	mu       sync.Mutex
	label    string
	buf      string
	lastLine string
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf += string(p)
	for {
		i := strings.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		w.buf = w.buf[i+1:]
		w.emitLocked(line)
	}
	return len(p), nil
}

// Flush emits a final line that never got its newline, which is how a process
// that dies mid-sentence still says what went wrong.
func (w *logWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf != "" {
		line := w.buf
		w.buf = ""
		w.emitLocked(line)
	}
}

func (w *logWriter) emitLocked(line string) {
	trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
	if trimmed == "" {
		return
	}
	w.lastLine = trimmed
	logger.Infof("%s | %s", w.label, trimmed)
}

func (w *logWriter) LastLine() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastLine
}

// Process is one supervised helper.
type Process struct {
	mu              sync.RWMutex
	cmd             *exec.Cmd
	done            chan struct{}
	binary          string
	args            []string
	log             *logWriter
	exitErr         error
	intentionalStop atomic.Bool
}

// New describes a process without starting it. label prefixes every line the
// child writes into the panel log.
func New(binary, label string, args ...string) *Process {
	return &Process{binary: binary, args: args, log: &logWriter{label: label}}
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
