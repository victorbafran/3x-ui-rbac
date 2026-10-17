//go:build linux

package ocserv

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// killStrayOcservProcesses terminates ocserv processes left over from a
// previous x-ui run and returns how many were killed.
//
// A survivor keeps holding the inbound port, so the freshly started instance
// cannot bind and the inbound looks enabled while nothing serves it.
//
// Matching is on our own runtime directory appearing in the command line, not
// on the binary name. ocserv is a system package an operator may legitimately
// be running for something else; killing every ocserv on the host would take
// down a service the panel never created.
func killStrayOcservProcesses(dir string) int {
	if dir == "" {
		return 0
	}
	needle := []byte(dir)
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	killed := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil || !bytes.Contains(data, needle) {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			killed++
		}
	}
	return killed
}
