package openvpn

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Seen live on 2026-10-22: a server the manager had lost track of kept its tun
// device, and every start after it failed "busy" every ten seconds for an hour.

// procRoot is /proc; a variable so a test can lay out processes of its own.
var procRoot = "/proc"

// serves reports whether a command line, NUL-separated as /proc holds it, is
// an openvpn running the given config file.
func serves(cmdline []byte, config string) bool {
	args := bytes.Split(bytes.TrimRight(cmdline, "\x00"), []byte{0})
	if len(args) == 0 || filepath.Base(string(args[0])) != "openvpn" {
		return false
	}
	for i := 1; i+1 < len(args); i++ {
		if string(args[i]) == "--config" && string(args[i+1]) == config {
			return true
		}
	}
	return false
}

// strays lists the processes serving config, none of which the manager holds
// by the time it is about to start one.
func strays(config string) []int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cmdline"))
		if err == nil && serves(cmdline, config) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// stopStrays ends every server of config the manager lost, as Stop would: ask,
// then kill, and wait until the tun device is free again.
func stopStrays(config string) {
	if runtime.GOOS != "linux" {
		return
	}
	for _, pid := range strays(config) {
		logger.Warningf("openvpn: pid %d was serving %s without the panel holding it; stopping it so the inbound can start", pid, config)
		p, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		_ = p.Signal(syscall.SIGTERM)
		if !waitGone(pid, 5*time.Second) {
			_ = p.Kill()
			waitGone(pid, 2*time.Second)
		}
	}
}

// gone is true once a process has exited; a zombie has, it is only unreaped.
func gone(pid int) bool {
	stat, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	i := bytes.LastIndexByte(stat, ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}

func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if gone(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
