package sidecar

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var openvpnLike = Package{
	Name:   "openvpn",
	Units:  []string{"openvpn.service", "openvpn@.service", "openvpn-server@.service"},
	OptOut: "XUI_WITH_OPENVPN",
}

func countingInstaller(runs *atomic.Int32) *Installer {
	return &Installer{goos: "linux", timeout: time.Minute, run: func(context.Context, string, string) (string, int, error) {
		runs.Add(1)
		return "", 0, nil
	}}
}

// The job asks every tick; apt-get holds the dpkg lock for a minute and a failed
// install would only fail again, so one panel run tries a package once.
func TestAMissingPackageIsInstalledOncePerRun(t *testing.T) {
	var runs atomic.Int32
	inst := countingInstaller(&runs)
	p := openvpnLike
	p.Present = func() bool { return false }

	done := inst.Request(p)
	if done == nil {
		t.Fatal("a missing package started no install")
	}
	<-done
	for range 3 {
		if again := inst.Request(p); again != nil {
			<-again
		}
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("installed %d times, want once", got)
	}
}

// XUI_WITH_OPENVPN=0 at install time is the operator saying no, and an installed
// package needs nothing; neither runs apt-get.
func TestNoInstallWhenPresentOrRefused(t *testing.T) {
	cases := []struct {
		name    string
		present bool
		env     string
	}{
		{"already installed", true, ""},
		{"opted out with 0", false, "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XUI_WITH_OPENVPN", tc.env)
			var runs atomic.Int32
			p := openvpnLike
			p.Present = func() bool { return tc.present }
			if done := countingInstaller(&runs).Request(p); done != nil {
				<-done
			}
			if got := runs.Load(); got != 0 {
				t.Fatalf("apt-get ran %d times", got)
			}
		})
	}
}

// runScriptWithStubs puts apt-get and systemctl stand-ins on an otherwise empty PATH;
// each logs its arguments, and apt-get fails its first `install` when told to.
func runScriptWithStubs(t *testing.T, withApt, firstInstallFails bool) (string, int) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to run the install script with")
	}
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")
	stub := "#!/bin/sh\necho \"${0##*/} $*\" >> '" + filepath.ToSlash(logFile) + "'\n"
	if firstInstallFails {
		marker := filepath.ToSlash(filepath.Join(dir, "failed-once"))
		stub += "case \"$1\" in install) [ -e '" + marker + "' ] || { : > '" + marker + "'; exit 100; };; esac\n"
	}
	names := []string{"systemctl"}
	if withApt {
		names = append(names, "apt-get")
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(sh, "-c", installScript(openvpnLike))
	// Nothing but the stand-ins: a real apt-get on PATH would install for real.
	cmd.Env = append(os.Environ(), "PATH="+dir)
	out, err := cmd.CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run the script: %v: %s", err, out)
	}
	calls, _ := os.ReadFile(logFile)
	return strings.ReplaceAll(string(calls), "\r", ""), code
}

// The distribution's openvpn service and its templates are what could take 1194
// before the panel's server: every one is stopped and masked after the install.
func TestTheInstallScriptInstallsAndMasksTheDistroServices(t *testing.T) {
	calls, code := runScriptWithStubs(t, true, false)
	if code != 0 {
		t.Fatalf("exit %d; calls:\n%s", code, calls)
	}
	for _, want := range []string{
		"apt-get install -y -q -o DPkg::Lock::Timeout=120 openvpn\n",
		"systemctl stop openvpn.service openvpn@* openvpn-server@*\n",
		"systemctl disable openvpn.service openvpn@.service openvpn-server@.service\n",
		"systemctl mask openvpn.service openvpn@.service openvpn-server@.service\n",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("no %q in the calls:\n%s", strings.TrimSpace(want), calls)
		}
	}
}

// A fresh image has no package lists, and apt-get then knows no openvpn at all.
func TestTheInstallScriptRefreshesTheListsWhenTheFirstTryFails(t *testing.T) {
	calls, code := runScriptWithStubs(t, true, true)
	if code != 0 {
		t.Fatalf("exit %d; calls:\n%s", code, calls)
	}
	want := "apt-get install -y -q -o DPkg::Lock::Timeout=120 openvpn\n" +
		"apt-get update -q\n" +
		"apt-get install -y -q -o DPkg::Lock::Timeout=120 openvpn\n"
	if !strings.HasPrefix(calls, want) {
		t.Fatalf("calls:\n%s\nwant them to start with:\n%s", calls, want)
	}
}

// Only apt-get is driven from the panel; any other host is told to do it by hand.
func TestTheInstallScriptRefusesAHostWithoutApt(t *testing.T) {
	calls, code := runScriptWithStubs(t, false, false)
	if code != errNoApt {
		t.Fatalf("exit %d, want %d", code, errNoApt)
	}
	if calls != "" {
		t.Fatalf("it went on to run:\n%s", calls)
	}
}
