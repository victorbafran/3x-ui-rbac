package tunnel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A restart drops every customer riding the tunnel at that moment, so the
// manager must only restart when the process would actually read something
// different. Each of these is a value the client reads once, at startup.
func TestOnlyAStartupChangeRestartsTheClient(t *testing.T) {
	base := goodPair()
	for _, tc := range []struct {
		name string
		edit func(*Pair)
	}{
		{"the front's address", func(p *Pair) { p.FrontAddress = "203.0.113.9" }},
		{"the tunnel port", func(p *Pair) { p.TunnelPort = 3081 }},
		{"the transport", func(p *Pair) { p.Transport = TransportWS }},
		{"the token", func(p *Pair) { p.Token = "a-different-token" }},
	} {
		changed := base
		tc.edit(&changed)
		if fingerprint(base) == fingerprint(changed) {
			t.Errorf("%s did not move the fingerprint, so the running client would keep dialling the old one", tc.name)
		}
	}

	// The customer-facing ports are the front's business. The panel's client
	// does not read them, so changing them must not drop the tunnel.
	morePorts := base
	morePorts.Ports = append(append([]int{}, base.Ports...), 8443)
	if fingerprint(base) != fingerprint(morePorts) {
		t.Error("adding a forwarded port moved the client's fingerprint, which would drop every customer on the tunnel to open a port on the other end")
	}
}

// The token is in this file and it is the whole of the tunnel's
// authentication. Every account on the host should not be able to read it.
func TestTheClientConfigIsNotReadableByEveryone(t *testing.T) {
	dir := t.TempDir()
	runtimeDirOverride = dir
	t.Cleanup(func() { runtimeDirOverride = "" })

	config, err := ClientConfig(goodPair())
	if err != nil {
		t.Fatalf("client config: %v", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := LocalConfigPath(goodPair().Id)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode is %o; the token in this file would be readable by other accounts", mode)
	}
	if !strings.Contains(config, goodPair().Token) {
		t.Error("the token is not in the config, so the two ends cannot authenticate")
	}
}

// Two tunnels on one panel must not share a config path: writing the second
// would silently retarget the first.
func TestTwoTunnelsDoNotShareALocalConfig(t *testing.T) {
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	if LocalConfigPath(1) == LocalConfigPath(2) {
		t.Fatalf("both tunnels write %q", LocalConfigPath(1))
	}
	if !strings.Contains(filepath.Base(LocalConfigPath(7)), "7") {
		t.Error("the tunnel's id is not in its config name")
	}
}

// A missing binary is a different thing from a tunnel that will not come up,
// and the operator can act on one and not the other.
func TestAMissingBinaryIsReportedAsMissingRatherThanAsAFailedTunnel(t *testing.T) {
	original := binaryCandidates
	binaryCandidates = []string{filepath.Join(t.TempDir(), "definitely-not-here")}
	t.Cleanup(func() { binaryCandidates = original })
	serveBackhaul(t, nil, nil)
	backhaulReleaseBase += "gone/"

	if got := BinaryPath(); got != "" {
		t.Fatalf("BinaryPath returned %q for a path that does not exist", got)
	}

	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })
	err := GetManager().Ensure(goodPair())
	if err == nil {
		t.Fatal("a tunnel was reported as started with no binary to start")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("it failed for some other reason than the missing binary: %v", err)
	}
}

// A directory is not a binary. Stat alone would accept one and the start
// would fail later with something that reads as a broken tunnel.
func TestADirectoryIsNotMistakenForTheBinary(t *testing.T) {
	dir := t.TempDir()
	asDir := filepath.Join(dir, "backhaul")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	original := binaryCandidates
	binaryCandidates = []string{asDir}
	t.Cleanup(func() { binaryCandidates = original })

	if got := BinaryPath(); got != "" {
		t.Fatalf("BinaryPath returned the directory %q", got)
	}
}
