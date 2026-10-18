//go:build !linux

package ocserv

// capabilityError is nil off Linux, where ocserv does not run and Available()
// has already refused the inbound.
func capabilityError() error { return nil }
