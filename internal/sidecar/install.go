package sidecar

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Package is a distribution package a sidecar runs from, with the distribution's
// own services for it, which must never start beside the panel's processes.
type Package struct {
	Name    string
	Present func() bool
	// Units are masked after the install; a template ("x@.service") masks every instance.
	Units []string
	// OptOut names the install.sh variable that, set to 0, keeps the package off.
	OptOut string
}

// errNoApt is the script's exit code on a host without apt-get.
const errNoApt = 100

// Installer installs a missing package at most once per panel run: apt-get is
// slow and holds the dpkg lock, and a failure would only repeat every tick.
type Installer struct {
	mu      sync.Mutex
	tried   map[string]bool
	goos    string
	timeout time.Duration
	run     func(ctx context.Context, name, script string) (string, int, error)
}

// Installs is the panel's installer.
var Installs = &Installer{goos: runtime.GOOS, timeout: 10 * time.Minute, run: runInstallScript}

// Request starts installing p in the background when it is missing. The channel
// closes when that install ends; nil means none was started.
func (i *Installer) Request(p Package) <-chan struct{} {
	if i.goos != "linux" || p.Present() || optedOut(p.OptOut) {
		return nil
	}
	i.mu.Lock()
	if i.tried == nil {
		i.tried = map[string]bool{}
	}
	if i.tried[p.Name] {
		i.mu.Unlock()
		return nil
	}
	i.tried[p.Name] = true
	i.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		i.install(p)
	}()
	return done
}

func (i *Installer) install(p Package) {
	logger.Infof("%s is not installed and an enabled inbound needs it; installing it with apt-get", p.Name)
	ctx, cancel := context.WithTimeout(context.Background(), i.timeout)
	defer cancel()
	out, code, err := i.run(ctx, p.Name, installScript(p))
	switch {
	case code == errNoApt:
		logger.Warningf("%s is not installed and this server has no apt-get; install it with its package manager", p.Name)
	case err != nil:
		logger.Warningf("installing %s failed: %v; it ended with: %s — install it by hand (apt-get install %s)",
			p.Name, err, lastLines(out, 3), p.Name)
	case !p.Present():
		logger.Warningf("apt-get installed %s but the panel still cannot find it; it ended with: %s", p.Name, lastLines(out, 3))
	default:
		logger.Infof("%s installed; its own services are masked and the panel starts it on the next pass", p.Name)
	}
}

// installScript installs the package with apt-get and masks the distribution's
// services, so its own server never takes the port an inbound listens on.
func installScript(p Package) string {
	var stop []string
	for _, u := range p.Units {
		if tmpl, ok := strings.CutSuffix(u, "@.service"); ok {
			stop = append(stop, "'"+tmpl+"@*'")
			continue
		}
		stop = append(stop, u)
	}
	install := "apt-get install -y -q -o DPkg::Lock::Timeout=120 " + p.Name + " </dev/null"
	units := strings.Join(p.Units, " ")
	return fmt.Sprintf(`command -v apt-get >/dev/null 2>&1 || exit %d
export DEBIAN_FRONTEND=noninteractive
%s || { apt-get update -q </dev/null && %s; } || exit 1
systemctl stop %s >/dev/null 2>&1
systemctl disable %s >/dev/null 2>&1
systemctl mask %s >/dev/null 2>&1
exit 0
`, errNoApt, install, install, strings.Join(stop, " "), units, units)
}

// runInstallScript runs outside the panel's own sandbox, which keeps /usr and
// /etc read-only, through a transient unit as the panel updater does.
func runInstallScript(ctx context.Context, name, script string) (string, int, error) {
	if systemdRun, err := exec.LookPath("systemd-run"); err == nil {
		unit := fmt.Sprintf("x-ui-install-%s-%d", name, time.Now().Unix())
		out, err := exec.CommandContext(ctx, systemdRun, "--unit", unit, "--wait", "--pipe", "--collect", "--quiet",
			"-p", "RuntimeMaxSec=600", "/bin/sh", "-c", script).CombinedOutput()
		if err == nil || !noSystemd(string(out)) {
			return string(out), exitCode(err), err
		}
	}
	out, err := exec.CommandContext(ctx, "/bin/sh", "-c", script).CombinedOutput()
	return string(out), exitCode(err), err
}

func noSystemd(out string) bool {
	return strings.Contains(out, "System has not been booted with systemd") || strings.Contains(out, "Failed to connect to bus")
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return 0
}

func optedOut(env string) bool {
	if env == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(env))) {
	case "0", "false", "no":
		return true
	}
	return false
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
