//go:build windows

package sidecar

import "os"

// terminate falls back to a kill off Unix. The servers supervised here do not
// run on Windows — each protocol refuses an inbound there — so this exists
// only to keep the package building for the panel's Windows target.
func terminate(p *os.Process) error { return p.Kill() }
