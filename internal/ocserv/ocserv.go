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

	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
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

// DistroPackage is what the panel installs when an enabled cisco inbound finds
// no ocserv; the distribution's own ocserv would take port 443 the moment it starts.
var DistroPackage = sidecar.Package{
	Name:    "ocserv",
	Present: func() bool { return ServerBinaryPath() != "" && CtlBinaryPath() != "" },
	Units:   []string{"ocserv.service"},
	OptOut:  "XUI_WITH_CISCO",
}

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
	// Checked last: the binaries being absent is the likelier problem and the
	// simpler message, and there is no point naming capabilities for a server
	// that has nothing to run.
	return capabilityError()
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

// Dir is runtimeDir, for a full backup to find each inbound's keys.
func Dir() string { return runtimeDir() }

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

func keyPathForID(id int) string { return filepath.Join(runtimeDir(), fmt.Sprintf("key-%d.pem", id)) }

// perUserDirForID holds one file per client that has its own limits.
// ocserv reads it at authentication time, so a limit changed here applies
// to the next login without restarting anything or dropping the clients
// already connected — measured on ocserv 1.2.4.
func perUserDirForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("peruser-%d", id))
}

// perGroupDirForID holds one file per country the inbound offers, named after
// the group. ocserv reads the file when a client picks that group, so the pool
// and resolvers behind a country can change without restarting the instance —
// unlike the select-group list itself, which is read once at startup.
func perGroupDirForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("pergroup-%d", id))
}

// ensureRuntimeDir creates the runtime directory and every parent it has to
// create along the way, each one traversable by the unprivileged user ocserv
// drops its workers to.
//
// Chmod'ing only the leaf is not enough, and the way it fails is invisible.
// The panel's systemd unit sets UMask=0077, so MkdirAll creates /var/lib/x-ui
// as 0700 while the explicit chmod opens only /var/lib/x-ui/ocserv beneath it.
// The socket inside then has the right owner and mode, and the worker still
// cannot reach it: a directory it cannot traverse is as good as a wall. What
// the operator sees is "Permission denied" on a socket whose permissions are
// plainly correct, and what the customer sees is a failed handshake.
//
// Directories that already exist are left exactly as they are — this widens
// only what it creates itself.
func ensureRuntimeDir() error {
	if err := mkdirTraversable(runtimeDir()); err != nil {
		return err
	}
	return traversabilityError(runtimeDir())
}

// mkdirTraversable is MkdirAll with the mode applied to each directory it
// creates, instead of to none of them (MkdirAll's perm argument is masked by
// the umask, and it never chmods).
func mkdirTraversable(dir string) error {
	var missing []string
	for p := filepath.Clean(dir); ; {
		st, err := os.Stat(p)
		if err == nil {
			if !st.IsDir() {
				return fmt.Errorf("%s exists and is not a directory", p)
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		if err := os.Chmod(missing[i], 0o755); err != nil {
			return err
		}
	}
	return os.Chmod(dir, 0o755)
}

// traversabilityError names the first directory on the way to dir that the
// unprivileged worker cannot enter. Reported rather than silently widened:
// an operator who pointed XUI_OCSERV_DIR somewhere deliberate should be told
// which directory is in the way, not have its permissions changed for them.
func traversabilityError(dir string) error {
	var parts []string
	for p := filepath.Clean(dir); ; {
		parts = append(parts, p)
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	for i := len(parts) - 1; i >= 0; i-- {
		st, err := os.Stat(parts[i])
		if err != nil {
			return nil
		}
		if st.Mode().Perm()&0o001 == 0 {
			return fmt.Errorf(
				"%s is not traversable by other users (mode %04o), so ocserv's unprivileged workers cannot reach their own socket under %s; "+
					"clients will fail the TLS handshake with nothing in the panel to explain it. Open it with: chmod o+x %s",
				parts[i], st.Mode().Perm(), dir, parts[i])
		}
	}
	return nil
}
