//go:build !linux

package ocserv

// killStrayOcservProcesses is a no-op off Linux, where ocserv does not run.
func killStrayOcservProcesses(_ string) int { return 0 }
