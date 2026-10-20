//go:build !windows

package sidecar

import (
	"os"
	"syscall"
)

// terminate asks the child to shut down cleanly, so a server tears its
// sessions down rather than abandoning them with their tun devices still up.
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
