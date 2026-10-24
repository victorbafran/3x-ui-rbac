// Package openvpn serves OpenVPN inbounds from a sidecar process, one per
// inbound, the same shape as internal/ocserv serves cisco.
//
// It differs from cisco in one way the operator feels: OpenVPN has no notion
// of a group the client picks before connecting, so where a cisco inbound
// offers several countries in one dropdown, an OpenVPN inbound is one country
// and a customer who has several holds several .ovpn files. The exit-server
// machinery underneath is the same.
package openvpn

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// binaryNames are what the distributions call it. openvpn is the only one in
// practice, but a path set by the operator wins over all of them.
var binaryNames = []string{"openvpn"}

var (
	lookupOnce sync.Once
	lookupPath string
	lookupErr  error
)

// BinaryPath is the openvpn executable this host will run, found once.
func BinaryPath() (string, error) {
	lookupOnce.Do(func() {
		if custom := strings.TrimSpace(os.Getenv("XUI_OPENVPN_BIN")); custom != "" {
			if _, err := os.Stat(custom); err == nil {
				lookupPath = custom
				return
			}
			lookupErr = fmt.Errorf("XUI_OPENVPN_BIN points at %s, which is not there", custom)
			return
		}
		for _, name := range binaryNames {
			if p, err := exec.LookPath(name); err == nil {
				lookupPath = p
				return
			}
		}
		lookupErr = fmt.Errorf("openvpn is not installed on this host")
	})
	return lookupPath, lookupErr
}

// Available reports whether this host can serve an OpenVPN inbound at all.
//
// The message is what an operator sees in the panel, so it says what to do
// rather than what is missing: "not found in PATH" sends somebody looking for
// a path problem when the package is simply not installed.
func Available() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("OpenVPN inbounds are served on linux only; this panel is running on %s", runtime.GOOS)
	}
	if _, err := BinaryPath(); err != nil {
		return fmt.Errorf("%w — install it with your package manager (apt install openvpn) and the inbound will start on its own", err)
	}
	return nil
}

// runtimeDirOverride lets tests point the package at a temporary directory.
var runtimeDirOverride string

// runtimeDir holds everything the sidecars read: configs, credentials,
// certificates and the sockets their management interfaces listen on.
//
// /var/lib rather than /etc, because the panel's own systemd unit runs with
// ProtectSystem=full and may not write /etc outside its own directory.
// Dir is runtimeDir, for a full backup to find each inbound's keys.
func Dir() string { return runtimeDir() }

func runtimeDir() string {
	if runtimeDirOverride != "" {
		return runtimeDirOverride
	}
	if dir := os.Getenv("XUI_OPENVPN_DIR"); dir != "" {
		return dir
	}
	return "/var/lib/x-ui/openvpn"
}

func configPathForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("openvpn-%d.conf", id))
}

func statusPathForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("status-%d.log", id))
}

// managementSocketForID is where the running process answers questions about
// who is connected and how much they have moved. A unix socket rather than a
// TCP port: a port on localhost is reachable by every process on the host,
// and this one can disconnect any customer.
func managementSocketForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("mgmt-%d.sock", id))
}

// passwdPathForID holds one line per client, login:hash, the same format and
// the same bcrypt cost as the cisco side. A customer with both protocols has
// one password, which is what their subscription page shows them.
func passwdPathForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("ovpasswd-%d", id))
}

func caCertPathForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("ca-%d.pem", id))
}

func certPathForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("cert-%d.pem", id))
}

func keyPathForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("key-%d.pem", id))
}

// tlsAuthPathForID holds the shared HMAC key. Every packet that does not carry
// its signature is dropped before OpenVPN looks at it, which keeps a scanner
// from learning that there is an OpenVPN here at all — and keeps the TLS stack
// out of reach of anybody who has not got the key.
func tlsAuthPathForID(id int) string {
	return filepath.Join(runtimeDir(), fmt.Sprintf("tls-crypt-%d.key", id))
}

// ensureRuntimeDir creates the runtime directory and every parent it has to
// create along the way, each one traversable by the unprivileged user the
// sidecar drops to.
//
// Chmod'ing only the leaf is not enough and the way it fails is invisible: the
// panel's unit sets UMask=0077, so a parent created along the way is 0700 and
// the dropped process cannot traverse it, however open the leaf is. This cost
// an evening on the cisco side; it is not being rediscovered here.
func ensureRuntimeDir() error {
	dir := runtimeDir()
	if err := mkdirTraversable(dir); err != nil {
		return err
	}
	return os.Chmod(dir, 0o755)
}

func mkdirTraversable(dir string) error {
	if dir == "" {
		return fmt.Errorf("no runtime directory")
	}
	parent := filepath.Dir(dir)
	if parent != dir && parent != "." && parent != string(filepath.Separator) {
		if _, err := os.Stat(parent); os.IsNotExist(err) {
			if err := mkdirTraversable(parent); err != nil {
				return err
			}
			// Only a directory this package created is opened up. One that
			// was already there belongs to somebody else.
			if err := os.Chmod(parent, 0o755); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("prepare %s: %w", dir, err)
	}
	return nil
}
