package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
)

// Server is one relay as the panel knows it.
type Server struct {
	Id      int
	Name    string
	Address string
	Port    int
	User    string

	// KeyPath is the panel's SSH key for this relay, written at enrollment.
	KeyPath string

	// WgPrivate is the panel's end of the tunnel; WgPeerPublic is the relay's.
	// Both are settled at enrollment and reused, so a reconcile does not
	// rekey a working tunnel and drop every customer on that country.
	WgPrivate    string
	WgPeerPublic string
}

// NewPanelKey generates the panel's own half of a tunnel. It is made once per
// relay and kept: rekeying the panel side would drop every customer currently
// out through that country for no reason.
func NewPanelKey() (private string, err error) {
	priv, _, err := wireguard.GenerateWireguardKeypair()
	if err != nil {
		return "", fmt.Errorf("generate the panel's tunnel key: %w", err)
	}
	return priv, nil
}

// GenerateSSHKey returns a new ed25519 key: the PEM the panel keeps and the
// authorized_keys line the relay gets.
func GenerateSSHKey(comment string) (privatePEM []byte, authorizedLine string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", err
	}
	return pem.EncodeToMemory(block), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), nil
}

// installKeyScript appends the panel's key to authorized_keys without
// disturbing whatever is already there, and without adding it twice when an
// operator re-enrolls the same server.
const installKeyScript = `#!/bin/bash
set -eu
LINE="$1"
HOME_DIR=$(getent passwd "$(id -un)" | cut -d: -f6)
[ -n "$HOME_DIR" ] || HOME_DIR=/root
mkdir -p "$HOME_DIR/.ssh"
chmod 700 "$HOME_DIR/.ssh"
touch "$HOME_DIR/.ssh/authorized_keys"
chmod 600 "$HOME_DIR/.ssh/authorized_keys"
grep -qxF "$LINE" "$HOME_DIR/.ssh/authorized_keys" || echo "$LINE" >> "$HOME_DIR/.ssh/authorized_keys"
echo "key in place at $HOME_DIR/.ssh/authorized_keys"
`

// Enroll uses the operator's password once: it installs a key, proves the key
// works by reconnecting with it, and writes it where only the panel can read
// it. The password is not returned, stored or logged.
//
// Proving the key before the password is thrown away matters. A server whose
// sshd refuses public keys would otherwise be enrolled, show as healthy, and
// then be unreachable the first time the panel needed it.
func Enroll(ctx context.Context, c Credentials, keyPath string) error {
	if strings.TrimSpace(c.Password) == "" && len(c.PrivateKey) == 0 {
		return fmt.Errorf("enrolling %s needs a password once", c.Address)
	}
	client, err := Dial(ctx, c)
	if err != nil {
		return err
	}
	defer client.Close()

	privatePEM, line, err := GenerateSSHKey("x-ui-relay")
	if err != nil {
		return err
	}
	if _, err := client.RunScript(ctx, "xui-relay-key.sh", installKeyScript, line); err != nil {
		return fmt.Errorf("could not install the panel's key on %s: %w", c.Address, err)
	}

	proof := Credentials{Address: c.Address, Port: c.Port, User: c.User, PrivateKey: privatePEM}
	check, err := Dial(ctx, proof)
	if err != nil {
		return fmt.Errorf("the key was installed on %s but it will not accept it, so the password would be the only way in: %w", c.Address, err)
	}
	check.Close()

	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return fmt.Errorf("prepare %s: %w", filepath.Dir(keyPath), err)
	}
	if err := os.WriteFile(keyPath, privatePEM, 0o600); err != nil {
		return fmt.Errorf("store the key: %w", err)
	}
	return nil
}

// Provision brings the tunnel up at both ends and returns the relay's public
// key, which the caller stores. The relay is set up first: the panel's end has
// nothing to talk to until the relay is listening.
//
// Only the relay's half is regenerated here. The panel keeps the private key
// it was enrolled with, and the relay never sees it — what travels is the
// relay's own private key and the panel's public one, which is the only split
// that lets either end be rebuilt without trusting the other with a secret it
// should not hold.
func Provision(ctx context.Context, s Server) (peerPublic string, err error) {
	panelPublic, err := wireguard.PublicKeyFromPrivate(s.WgPrivate)
	if err != nil {
		return "", fmt.Errorf("the stored tunnel key for %s is unusable; enroll it again: %w", s.Name, err)
	}
	relayPrivate, relayPublic, err := wireguard.GenerateWireguardKeypair()
	if err != nil {
		return "", fmt.Errorf("generate the relay's tunnel key: %w", err)
	}

	key, err := os.ReadFile(s.KeyPath)
	if err != nil {
		return "", fmt.Errorf("the key for %s is missing; enroll it again: %w", s.Name, err)
	}
	client, err := Dial(ctx, Credentials{Address: s.Address, Port: s.Port, User: s.User, PrivateKey: key})
	if err != nil {
		return "", err
	}
	defer client.Close()

	_, relaySide := tunnelAddress(s.Id)
	out, err := client.RunScript(ctx, "xui-relay-up.sh", remoteScript,
		relayPrivate, panelPublic, relaySide, strconv.Itoa(ListenPort(s.Id)), exitPools, InterfaceName(s.Id))
	if err != nil {
		return "", fmt.Errorf("setting up %s: %w", s.Name, err)
	}
	if !strings.Contains(out, "exit ready") {
		return "", fmt.Errorf("setting up %s did not finish: %s", s.Name, firstLines(out, 2))
	}

	s.WgPeerPublic = relayPublic
	if err := bringUpLocal(s); err != nil {
		return "", err
	}
	return relayPublic, nil
}

// Teardown removes the panel's footprint from a relay being given up, then
// takes the local end down. A failure on the relay is reported but does not
// stop the local cleanup: the operator may be removing it precisely because
// the server is already gone.
func Teardown(ctx context.Context, s Server) error {
	downLocal(s)
	key, err := os.ReadFile(s.KeyPath)
	if err != nil {
		return nil
	}
	client, err := Dial(ctx, Credentials{Address: s.Address, Port: s.Port, User: s.User, PrivateKey: key})
	if err != nil {
		return err
	}
	defer client.Close()
	_, err = client.RunScript(ctx, "xui-relay-down.sh", teardownScript, exitPools, InterfaceName(s.Id))
	_ = os.Remove(s.KeyPath)
	return err
}

// localDir holds the panel's own tunnel configs. Not /etc/wireguard: the
// panel's systemd unit runs with ProtectSystem=full, so /etc is read-only
// except for its own directory, and wg-quick takes a path just as happily as
// an interface name.
func localDir() string {
	if dir := os.Getenv("XUI_RELAY_DIR"); dir != "" {
		return dir
	}
	return "/var/lib/x-ui/relay"
}

func localConfigPath(id int) string {
	return filepath.Join(localDir(), InterfaceName(id)+".conf")
}

// Up brings only the panel's end of an existing tunnel back, without touching
// the relay. A WireGuard interface does not survive a reboot while everything
// it needs — the keys, the endpoint — already does, so the cheap half is the
// one worth doing on its own.
func Up(s Server) error { return bringUpLocal(s) }

// Down takes the panel's end off, for a country an operator has switched off.
// The relay keeps its configuration and its key, so switching it back on does
// not ask for a password again.
func Down(s Server) { downLocal(s) }

func bringUpLocal(s Server) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if err := os.MkdirAll(localDir(), 0o700); err != nil {
		return fmt.Errorf("prepare %s: %w", localDir(), err)
	}
	panelSide, _ := tunnelAddress(s.Id)
	endpoint := fmt.Sprintf("%s:%d", s.Address, ListenPort(s.Id))
	body := localConfig(s.WgPrivate, s.WgPeerPublic, panelSide, endpoint)
	path := localConfigPath(s.Id)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// Down first: wg-quick refuses to bring up an interface that exists, and
	// an endpoint or key that has just changed has to replace the old one.
	_ = runLocal("wg-quick", "down", path)
	if out, err := runLocalOut("wg-quick", "up", path); err != nil {
		return fmt.Errorf("could not bring up %s here: %w: %s", InterfaceName(s.Id), err, firstLines(out, 2))
	}
	return nil
}

func downLocal(s Server) {
	if runtime.GOOS != "linux" {
		return
	}
	path := localConfigPath(s.Id)
	_ = runLocal("wg-quick", "down", path)
	_ = os.Remove(path)
}

const localTimeout = 30 * time.Second

func runLocal(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), localTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}

func runLocalOut(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), localTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}
