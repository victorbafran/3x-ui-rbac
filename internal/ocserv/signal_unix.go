//go:build !windows

package ocserv

import (
	"os"
	"syscall"
)

// terminate asks ocserv to shut down cleanly so live sessions are torn down
// rather than abandoned with their tun devices still up.
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
