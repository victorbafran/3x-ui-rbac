// Package ocserv manages ocserv (OpenConnect VPN server) sidecar processes that
// serve Cisco AnyConnect compatible inbounds. Xray-core has no AnyConnect
// protocol, so a cisco inbound is run as a standalone ocserv process — one per
// inbound, serving every active client out of a generated password file —
// entirely outside the Xray config and lifecycle.
//
// Unlike the Xray and mtg binaries, ocserv is not shipped with the panel: it is
// a distribution package. When it is absent the manager says so in plain words
// rather than failing silently, because an inbound that looks enabled while
// nothing listens is the worst of both.
package ocserv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// serverCandidates and ctlCandidates are the usual install locations, tried
// after PATH. ocserv lives in sbin, which is often missing from a service's
// PATH even though the binary is installed.
var (
	serverCandidates = []string{"/usr/sbin/ocserv", "/usr/local/sbin/ocserv", "/sbin/ocserv"}
	ctlCandidates    = []string{"/usr/bin/occtl", "/usr/local/bin/occtl", "/usr/sbin/occtl"}
)

func lookup(name string, candidates []string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// ServerBinaryPath returns the ocserv executable, or "" when it is not installed.
func ServerBinaryPath() string { return lookup("ocserv", serverCandidates) }

// CtlBinaryPath returns the occtl executable, or "" when it is not installed.
func CtlBinaryPath() string { return lookup("occtl", ctlCandidates) }

// Available reports why a cisco inbound cannot be served, or nil when it can.
// The messages name the package to install, because that is the only thing the
// operator can actually do about it.
func Available() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("cisco inbounds need a Linux host: ocserv does not run on %s", runtime.GOOS)
	}
	if ServerBinaryPath() == "" {
		return fmt.Errorf("ocserv is not installed on this server; install it with: apt-get install ocserv (Debian/Ubuntu) or dnf install ocserv (RHEL/Fedora)")
	}
	if CtlBinaryPath() == "" {
		return fmt.Errorf("occtl is not installed on this server; it ships with the ocserv package — reinstall ocserv")
	}
	return nil
}

// defaultRuntimeDir is deliberately outside the panel's own folder.
//
// ocserv drops to an unprivileged user: its worker connects to the sec-mod
// socket and sec-mod reads the password file, so every directory on the way
// must be traversable by that user. A panel installed under /root — mode 0700,
// which no amount of chmod on our own files can open up — fails the TLS
// handshake with nothing but a GnuTLS internal error, naming no cause. Keeping
// the runtime state under /var/lib sidesteps the whole class.
const defaultRuntimeDir = "/var/lib/x-ui/ocserv"

// runtimeDirOverride lets tests point the package at a temporary directory.
var runtimeDirOverride string

func runtimeDir() string {
	if runtimeDirOverride != "" {
		return runtimeDirOverride
	}
	if dir := os.Getenv("XUI_OCSERV_DIR"); dir != "" {
		return dir
	}
	return defaultRuntimeDir
}

func configPathForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("ocserv-%d.conf", id))
}
func passwdPathForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("ocpasswd-%d", id))
}
func ipcSocketForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("ipc-%d.sock", id))
}
func ctlSocketForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("ctl-%d.sock", id))
}
func certPathForID(id int) string { return filepath.Join(runtimeDir(), fmt.Sprintf("cert-%d.pem", id)) }
func keyPathForID(id int) string  { return filepath.Join(runtimeDir(), fmt.Sprintf("key-%d.pem", id)) }

// perUserDirForID holds one file per client that has its own limits.
// ocserv reads it at authentication time, so a limit changed here applies
// to the next login without restarting anything or dropping the clients
// already connected — measured on ocserv 1.2.4.
func perUserDirForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("peruser-%d", id))
}

// ensureRuntimeDir creates the runtime directory world-traversable. MkdirAll
// honours the umask, which on many systems strips exactly the bits the
// unprivileged worker needs, so the mode is set explicitly afterwards.
func ensureRuntimeDir() error {
	dir := runtimeDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.Chmod(dir, 0o755)
}
