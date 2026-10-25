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

// signalPid is how a stray is signalled; a variable so a test can refuse it,
// as the kernel does a panel without CAP_KILL once openvpn dropped to nobody.
var signalPid = func(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

// stopStrays ends every server of an inbound the manager lost and returns how
// many are left: its own management socket first, then a signal.
func stopStrays(id int) int {
	if runtime.GOOS != "linux" {
		return 0
	}
	config := configPathForID(id)
	pids := strays(config)
	if len(pids) == 0 {
		return 0
	}
	for _, pid := range pids {
		logger.Warningf("openvpn: pid %d was serving %s without the panel holding it; stopping it so the inbound can start", pid, config)
	}
	if askToExit(id) == nil {
		for _, pid := range pids {
			waitGone(pid, stopGrace)
		}
	}
	left := 0
	for _, pid := range pids {
		if gone(pid) {
			continue
		}
		_ = signalPid(pid, syscall.SIGTERM)
		if !waitGone(pid, 5*time.Second) {
			_ = signalPid(pid, syscall.SIGKILL)
			if !waitGone(pid, 2*time.Second) {
				left++
			}
		}
	}
	return left
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
