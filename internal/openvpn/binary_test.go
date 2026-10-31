package openvpn

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The panel installs openvpn while it runs; a miss remembered from before the
// install would keep every inbound down until somebody restarted the panel.
func TestOpenVPNInstalledWhileThePanelRunsIsFound(t *testing.T) {
	t.Setenv("XUI_OPENVPN_BIN", "")
	lookupPath = ""
	t.Cleanup(func() { lookupPath = "" })
	bin := t.TempDir()
	t.Setenv("PATH", bin)

	if _, err := BinaryPath(); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("before the install: err = %v, want ErrNotInstalled", err)
	}
	name := "openvpn"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	want := filepath.Join(bin, name)
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := BinaryPath(); err != nil || got != want {
		t.Fatalf("after the install: BinaryPath = %q, %v; want %q", got, err, want)
	}
}
