package tunnel

import (
	"strings"
	"testing"
)

// The install script downloads a binary onto someone else's server and runs
// it as root. These are the lines that make that defensible, and a test that
// only checked "the script exists" would not notice any of them going.
func TestTheDownloadedBinaryIsVerifiedBeforeItIsRun(t *testing.T) {
	for what, want := range map[string]string{
		"nothing fetches the published checksums":   "checksums.txt",
		"the archive is never checked against them": "sha256sum -c",
		"a mismatch does not stop the install":      "does not match its published checksum",
		"the version is not used in the url":        "$VERSION",
	} {
		if !strings.Contains(remoteScript, want) {
			t.Errorf("%s: %q is missing from the install script", what, want)
		}
	}
	// The version arrives as an argument, so what has to be pinned is the
	// constant Provision passes. "latest" would hand two fronts set up months
	// apart different binaries, which is how two ends end up disagreeing
	// about the protocol.
	if strings.Contains(remoteScript, "releases/latest") {
		t.Error("the install follows latest; two fronts set up at different times would not match")
	}
	if !strings.HasPrefix(Release, "v") || strings.Contains(Release, "latest") {
		t.Errorf("Release is %q, which is not one fixed published version", Release)
	}
}

// An operator who re-saves a tunnel, or a reconcile after a reboot, runs this
// again. It must land on the same single unit rather than a second one, and
// must not swap the binary under a tunnel that is carrying traffic.
func TestInstallingTwiceIsSafe(t *testing.T) {
	if !strings.Contains(remoteScript, "NEED=1") || !strings.Contains(remoteScript, "NEED=0") {
		t.Error("the binary is fetched unconditionally, so every reconcile re-downloads it")
	}
	if !strings.Contains(remoteScript, "daemon-reload") {
		t.Error("systemd is never told the unit changed")
	}
}

// The script must report what is true rather than that it finished. A unit
// that started and exited immediately looks exactly like one that is serving,
// and the operator would be told the tunnel was up.
func TestProvisioningProvesThePortIsOpenBeforeClaimingSuccess(t *testing.T) {
	if !strings.Contains(remoteScript, "listening on") {
		t.Fatal("nothing confirms the tunnel port is actually open")
	}
	if !strings.Contains(remoteScript, "ss -lnt") {
		t.Error("the port is never looked at")
	}
	if !strings.Contains(remoteScript, "exit 1") {
		t.Error("a front that never listens still reports success")
	}
}

// Two tunnels can share one front machine. Removing the first must not take
// the binary out from under the second.
func TestTearingDownOneTunnelLeavesTheOthersRunning(t *testing.T) {
	if !strings.Contains(teardownScript, "backhaul-x-ui-*.service") {
		t.Fatal("teardown does not check for other tunnels before removing the binary")
	}
	before := strings.Index(teardownScript, "backhaul-x-ui-*.service")
	after := strings.Index(teardownScript, "rm -f /usr/local/bin/backhaul")
	if before < 0 || after < 0 || after < before {
		t.Error("the binary is removed before anything checks whether another tunnel needs it")
	}
}

// What the panel reports when something fails is the last thing the remote
// said, because everything before it is install chatter.
func TestTheReasonIsTakenFromTheEndOfTheOutput(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"installing\nunpacking\nthe downloaded backhaul does not match its published checksum\n", "the downloaded backhaul does not match its published checksum"},
		{"only one line", "only one line"},
		{"trailing blanks\n\n\n", "trailing blanks"},
		{"", ""},
	} {
		if got := lastLine(tc.in); got != tc.want {
			t.Errorf("lastLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
