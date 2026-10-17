//go:build windows

package ocserv

import "os"

// terminate falls back to a kill off Unix. ocserv does not run on Windows —
// Available() refuses a cisco inbound there — so this exists only to keep the
// package building for the panel's Windows target.
func terminate(p *os.Process) error { return p.Kill() }
