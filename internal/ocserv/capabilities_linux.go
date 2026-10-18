//go:build linux

package ocserv

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ocserv forks a worker per session and immediately drops it to an
// unprivileged user, which needs three capabilities the panel's own systemd
// sandbox does not grant:
//
//	CAP_SETGID, CAP_SETUID   setgid/setuid to the run-as user
//	CAP_CHOWN                hand the sec-mod socket to that user
//
// Without them the failure is silent where it matters. The panel starts, the
// port listens, the client reaches the server — and the worker dies before
// the TLS handshake completes, so the customer is told "server communication
// errors" and the operator is told nothing at all. Measured on ocserv 1.2.4:
// with only SETGID+SETUID the socket stays root-owned and the worker gets
// "Permission denied"; CAP_CHOWN is what completes the set.
const (
	capChown  = 0
	capSetgid = 6
	capSetuid = 7
)

var requiredCaps = []struct {
	bit  uint
	name string
}{
	{capSetgid, "CAP_SETGID"},
	{capSetuid, "CAP_SETUID"},
	{capChown, "CAP_CHOWN"},
}

// capBoundingSet reads this process's capability bounding set. A bounding set
// is subtracted from root's own privileges too, so running as root says
// nothing about whether these calls will succeed.
func capBoundingSet() (uint64, error) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "CapBnd:") {
			continue
		}
		hex := strings.TrimSpace(strings.TrimPrefix(line, "CapBnd:"))
		mask, perr := strconv.ParseUint(hex, 16, 64)
		if perr != nil {
			return 0, perr
		}
		return mask, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("no CapBnd line in /proc/self/status")
}

// missingCapabilities names the capabilities ocserv needs that this process
// cannot pass on. An unreadable bounding set returns none: refusing to serve
// on a guess would be worse than letting ocserv try and report for itself.
func missingCapabilities(mask uint64) []string {
	var missing []string
	for _, c := range requiredCaps {
		if mask&(1<<c.bit) == 0 {
			missing = append(missing, c.name)
		}
	}
	return missing
}

// capabilityError describes what to add and where, or nil when the process
// can already do what ocserv needs.
func capabilityError() error {
	mask, err := capBoundingSet()
	if err != nil {
		return nil
	}
	missing := missingCapabilities(mask)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"the panel's systemd sandbox withholds %s, which ocserv needs to drop its worker processes to an unprivileged user; "+
			"clients will reach the server and then fail the TLS handshake. Add them with:\n"+
			"  mkdir -p /etc/systemd/system/x-ui.service.d\n"+
			"  printf '[Service]\\nCapabilityBoundingSet=CAP_DAC_OVERRIDE CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW CAP_SETGID CAP_SETUID CAP_CHOWN\\n' > /etc/systemd/system/x-ui.service.d/20-cisco.conf\n"+
			"  systemctl daemon-reload && systemctl restart x-ui",
		strings.Join(missing, ", "))
}
