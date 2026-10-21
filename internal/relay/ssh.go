// Package relay provisions the servers that a country's traffic leaves
// through. A relay is an ordinary VPS with nothing of this panel installed on
// it: the panel reaches it over SSH, puts WireGuard and one forwarding rule
// there, and never touches it again except to check on it.
//
// The operator types a password once, when the relay is enrolled. It is used
// to install a key and is never written down: everything after enrollment goes
// through the key, so a panel database that leaks carries no password for
// anybody's server.
package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// dialTimeout is how long enrollment waits for a server that may be booting.
// A freshly created VPS can take a while to answer, and a five-second failure
// reads to the operator as a wrong password.
const dialTimeout = 20 * time.Second

// commandTimeout covers installing a package over a slow link.
const commandTimeout = 5 * time.Minute

// Credentials are how the panel reaches a relay. Exactly one of Password and
// PrivateKey is used: the password only at enrollment, the key forever after.
type Credentials struct {
	Address    string
	Port       int
	User       string
	Password   string
	PrivateKey []byte
}

func (c Credentials) addr() string {
	port := c.Port
	if port <= 0 {
		port = 22
	}
	return net.JoinHostPort(c.Address, strconv.Itoa(port))
}

func (c Credentials) user() string {
	if strings.TrimSpace(c.User) == "" {
		return "root"
	}
	return strings.TrimSpace(c.User)
}

// Client is one SSH session to a relay.
type Client struct {
	conn *ssh.Client
	host string
}

// Dial opens a session.
//
// The host key is accepted on first sight and not pinned. The alternative on a
// panel that enrolls machines the operator has just created is to refuse every
// new server until somebody copies a fingerprint by hand, which nobody would
// do; what the key would protect is a password that is used once over a link
// the operator chose. Enrollment replaces it with a key immediately.
func Dial(ctx context.Context, c Credentials) (*Client, error) {
	var auth []ssh.AuthMethod
	if len(c.PrivateKey) > 0 {
		signer, err := ssh.ParsePrivateKey(c.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("the stored key for %s cannot be read: %w", c.Address, err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if c.Password != "" {
		auth = append(auth, ssh.Password(c.Password),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = c.Password
				}
				return answers, nil
			}))
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("no password and no key for %s", c.Address)
	}

	cfg := &ssh.ClientConfig{
		User:            c.user(),
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         dialTimeout,
	}

	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.addr())
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", c.addr(), err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, c.addr(), cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh to %s refused: %w", c.addr(), err)
	}
	return &Client{conn: ssh.NewClient(sshConn, chans, reqs), host: c.Address}, nil
}

// Close ends the session.
func (c *Client) Close() {
	if c != nil && c.conn != nil {
		_ = c.conn.Close()
	}
}

// Run executes one command and returns what it printed. stderr is folded into
// the error because a relay that refuses to install something explains itself
// there and nowhere else, and an operator reading "exit status 100" learns
// nothing at all.
func (c *Client) Run(ctx context.Context, command string) (string, error) {
	return c.run(ctx, command, nil)
}

// Upload writes data to path on the relay through the session's input, which
// is how a binary far too large for a command line gets there. It is written
// beside the target and renamed over it, so nothing ever reads half a file.
func (c *Client) Upload(ctx context.Context, path string, data []byte) error {
	q := shellQuote(path)
	part := shellQuote(path + ".part")
	_, err := c.run(ctx, "umask 077; cat > "+part+" && mv -f "+part+" "+q, bytes.NewReader(data))
	return err
}

func (c *Client) run(ctx context.Context, command string, stdin io.Reader) (string, error) {
	if c == nil || c.conn == nil {
		return "", errors.New("no connection to the relay")
	}
	session, err := c.conn.NewSession()
	if err != nil {
		return "", err
	}
	defer func() { _ = session.Close() }()

	var out, errOut bytes.Buffer
	session.Stdout = &out
	session.Stderr = &errOut
	if stdin != nil {
		session.Stdin = stdin
	}

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	timeout := time.NewTimer(commandTimeout)
	defer timeout.Stop()
	select {
	case err := <-done:
		if err != nil {
			detail := strings.TrimSpace(errOut.String())
			if detail == "" {
				detail = strings.TrimSpace(out.String())
			}
			if detail != "" {
				return out.String(), fmt.Errorf("%w: %s", err, firstLines(detail, 3))
			}
			return out.String(), err
		}
		return out.String(), nil
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return out.String(), ctx.Err()
	case <-timeout.C:
		_ = session.Signal(ssh.SIGKILL)
		return out.String(), fmt.Errorf("%s took longer than %s", firstLines(command, 1), commandTimeout)
	}
}

// RunScript uploads a script and runs it, which is how anything longer than a
// line gets to a relay. Quoting a multi-line script through a command line is
// where the mistakes live.
func (c *Client) RunScript(ctx context.Context, name, body string, args ...string) (string, error) {
	path := "/tmp/" + name
	write := fmt.Sprintf("cat > %s <<'XUIEOF'\n%s\nXUIEOF\nchmod 700 %s", path, body, path)
	if _, err := c.Run(ctx, write); err != nil {
		return "", fmt.Errorf("could not place %s: %w", name, err)
	}
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, shellQuote(a))
	}
	out, err := c.Run(ctx, strings.TrimSpace(path+" "+strings.Join(quoted, " ")))
	_, _ = c.Run(ctx, "rm -f "+path)
	return out, err
}

// shellQuote makes one argument safe to pass through a shell. A relay name or
// an address comes from an operator's keyboard and reaches a root shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// firstLines keeps an error readable in a toast.
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "; ")
}
