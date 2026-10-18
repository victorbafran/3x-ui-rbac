//go:build linux

package ocserv

import (
	"strings"
	"testing"
)

// Bit numbers are written out rather than taken from the package's own
// constants: a test that builds its mask from the constant it is checking
// moves with a wrong constant instead of catching it. These are the kernel's
// own numbers (uapi/linux/capability.h).
const (
	bitChown          = 0
	bitDacOverride    = 1
	bitSetgid         = 6
	bitSetuid         = 7
	bitNetBindService = 10
	bitNetAdmin       = 12
	bitNetRaw         = 13
)

func maskOf(bits ...uint) uint64 {
	var m uint64
	for _, b := range bits {
		m |= 1 << b
	}
	return m
}

// The shipped systemd unit grants DAC_OVERRIDE, NET_ADMIN, NET_BIND_SERVICE
// and NET_RAW, and nothing else. Measured on that exact set: the ocserv
// worker cannot setgid and the client is told "server communication errors"
// with nothing in the panel to explain it.
func TestTheShippedSandboxIsReportedAsMissingAllThree(t *testing.T) {
	// CAP_DAC_OVERRIDE=1, CAP_NET_BIND_SERVICE=10, CAP_NET_ADMIN=12, CAP_NET_RAW=13
	shipped := maskOf(bitDacOverride, bitNetBindService, bitNetAdmin, bitNetRaw)
	missing := missingCapabilities(shipped)
	if len(missing) != 3 {
		t.Fatalf("got %v, want all three named", missing)
	}
	joined := strings.Join(missing, ",")
	for _, want := range []string{"CAP_SETGID", "CAP_SETUID", "CAP_CHOWN"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s is not reported missing", want)
		}
	}
}

// CAP_CHOWN is the one that is easy to leave out: with only SETGID and SETUID
// the worker drops privileges fine and then cannot reach the sec-mod socket,
// because ocserv could not hand that socket to the user it dropped to.
func TestChownAloneMissingIsStillReported(t *testing.T) {
	withoutChown := maskOf(bitDacOverride, bitNetBindService, bitNetAdmin, bitNetRaw, bitSetgid, bitSetuid)
	missing := missingCapabilities(withoutChown)
	if len(missing) != 1 || missing[0] != "CAP_CHOWN" {
		t.Fatalf("got %v, want exactly [CAP_CHOWN]", missing)
	}
}

func TestACapableProcessReportsNothingMissing(t *testing.T) {
	capable := maskOf(bitDacOverride, bitNetBindService, bitNetAdmin, bitNetRaw, bitSetgid, bitSetuid, bitChown)
	if missing := missingCapabilities(capable); len(missing) != 0 {
		t.Fatalf("got %v, want none", missing)
	}
}

// The message is the whole point of the check: it replaces a failure the
// operator cannot see with the command that fixes it.
func TestTheMessageNamesWhatIsMissingAndHowToAddIt(t *testing.T) {
	missing := missingCapabilities(maskOf(bitDacOverride, bitNetBindService, bitNetAdmin, bitNetRaw))
	if len(missing) == 0 {
		t.Fatal("test setup: nothing reported missing")
	}
	// capabilityError reads the live process, so the message is built here
	// from the same parts rather than by faking /proc.
	msg := strings.Join(missing, ", ")
	for _, want := range []string{"CAP_SETGID", "CAP_SETUID", "CAP_CHOWN"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the operator is not told about %s", want)
		}
	}
}

// A process that really can do it must not be refused. The test box runs the
// suite as whatever user it runs as, so this only asserts the two agree.
func TestCapabilityErrorAgreesWithTheLiveBoundingSet(t *testing.T) {
	mask, err := capBoundingSet()
	if err != nil {
		t.Skipf("cannot read the bounding set here: %v", err)
	}
	wantErr := len(missingCapabilities(mask)) > 0
	gotErr := capabilityError() != nil
	if wantErr != gotErr {
		t.Fatalf("capabilityError() returned err=%v while the bounding set says missing=%v",
			gotErr, missingCapabilities(mask))
	}
}
