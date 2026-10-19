package relay

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The panel's end of a relay tunnel carries AllowedIPs 0.0.0.0/0, because a
// customer in that country may ask for any destination at all. wg-quick's
// default is to turn AllowedIPs into routes, which would move the panel's own
// traffic — and the operator's ssh session — into a tunnel to a rented VPS the
// moment a country is added. Table = off is what stops that, and it is the one
// line in this package that must never go missing.
func TestThePanelEndNeverTakesOverTheDefaultRoute(t *testing.T) {
	cfg := localConfig("privkey", "peerpub", "10.200.1.1", "198.51.100.7:51821")
	if !strings.Contains(cfg, "AllowedIPs = 0.0.0.0/0") {
		t.Fatalf("the tunnel cannot carry arbitrary destinations:\n%s", cfg)
	}
	if !strings.Contains(cfg, "Table = off") {
		t.Fatalf("wg-quick would route everything into the relay, taking the host's own traffic with it:\n%s", cfg)
	}
}

// The endpoint has to be the relay's address on the relay's own port, or the
// handshake goes nowhere and the country simply never works.
func TestThePanelEndPointsAtTheRelay(t *testing.T) {
	cfg := localConfig("p", "q", "10.200.3.1", "203.0.113.9:51823")
	for _, want := range []string{
		"Address = 10.200.3.1/24",
		"PrivateKey = p",
		"PublicKey = q",
		"Endpoint = 203.0.113.9:51823",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("missing %q from the panel's config:\n%s", want, cfg)
		}
	}
}

// Two relays sharing an interface name, an address pair or a port would fight
// over the same tunnel, and the second one enrolled would take the first one
// down without saying so.
func TestEveryRelayGetsItsOwnInterfaceAddressAndPort(t *testing.T) {
	names := map[string]int{}
	addrs := map[string]int{}
	ports := map[int]int{}
	for id := 1; id <= 200; id++ {
		n := InterfaceName(id)
		if other, clash := names[n]; clash {
			t.Fatalf("relays %d and %d share the interface %s", other, id, n)
		}
		if len(n) > 15 {
			t.Fatalf("interface %q is longer than linux allows", n)
		}
		names[n] = id

		panelSide, relaySide := tunnelAddress(id)
		if panelSide == relaySide {
			t.Fatalf("relay %d has the same address at both ends", id)
		}
		for _, a := range []string{panelSide, relaySide} {
			if other, clash := addrs[a]; clash {
				t.Fatalf("relays %d and %d both use %s", other, id, a)
			}
			addrs[a] = id
		}

		p := ListenPort(id)
		if other, clash := ports[p]; clash {
			t.Fatalf("relays %d and %d both listen on %d", other, id, p)
		}
		ports[p] = id
	}
}

// The pool block the relay is told to accept and masquerade has to be the one
// the cisco side actually hands out, or a country comes up, hands a customer
// an address, and that address is dropped at the far end.
func TestTheRelayAcceptsEveryPoolACountryCanBeGiven(t *testing.T) {
	if exitPools != "10.248.0.0/13" {
		t.Fatalf("the relay is told about %s, which is not where cisco location pools live", exitPools)
	}
	if !strings.Contains(remoteScript, "MASQUERADE") {
		t.Fatal("the relay would forward the pool without translating it, so nothing would come back")
	}
}

// The relay script has to survive being run again: an operator who re-saves a
// server, or a reconcile after a reboot, must not end up with the rule twice.
func TestTheRelayScriptCanBeRunTwice(t *testing.T) {
	if !strings.Contains(remoteScript, "iptables -t nat -C POSTROUTING") {
		t.Fatal("the masquerade rule is added without checking, so it stacks on every run")
	}
	if !strings.Contains(remoteScript, "command -v wg >/dev/null") {
		t.Fatal("wireguard is installed again on every run")
	}
}

// Teardown is what lets an operator hand a server back. A relay released with
// a live tunnel and a masquerade rule is a machine still forwarding somebody's
// traffic after it has changed hands.
func TestTeardownRemovesWhatTheScriptAdded(t *testing.T) {
	for _, want := range []string{"wg-quick@", "rm -f", "POSTROUTING", "FORWARD"} {
		if !strings.Contains(teardownScript, want) {
			t.Errorf("teardown leaves %s behind", want)
		}
	}
}

// Names and addresses come from an operator's keyboard and reach a root shell
// on another machine. A quote that is not closed is a command that is.
func TestAnArgumentCannotEscapeIntoTheShell(t *testing.T) {
	got := shellQuote("'; rm -rf / #")
	if strings.HasPrefix(got, "'") && strings.HasSuffix(got, "'") {
		inner := got[1 : len(got)-1]
		if strings.Contains(strings.ReplaceAll(inner, `'\''`, ""), "'") {
			t.Fatalf("a bare quote survived quoting: %s", got)
		}
		return
	}
	t.Fatalf("the argument was not quoted at all: %s", got)
}

// The key the panel installs has to be one sshd will take and one the panel
// can read back, or enrollment succeeds and every later connection fails.
func TestTheGeneratedKeyIsUsableAtBothEnds(t *testing.T) {
	priv, line, err := GenerateSSHKey("x-ui-relay")
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	if _, err := ssh.ParsePrivateKey(priv); err != nil {
		t.Fatalf("the panel cannot read back its own key: %v", err)
	}
	if !strings.HasPrefix(line, "ssh-ed25519 ") {
		t.Fatalf("authorized_keys line is not a key sshd would take: %q", line)
	}
	if strings.Contains(line, "\n") {
		t.Fatal("the authorized_keys line spans two lines, which would corrupt the file")
	}
}

// Two enrollments must not produce the same key, or every relay the panel ever
// touches would accept the same one.
func TestEachEnrollmentGetsItsOwnKey(t *testing.T) {
	_, a, err := GenerateSSHKey("x-ui-relay")
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := GenerateSSHKey("x-ui-relay")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two enrollments produced the same key")
	}
}

// WireGuard is silent while idle, so "healthy" is a judgement about how long
// ago the ends last agreed, not about whether packets are flowing. Keepalive
// is 25 seconds, so a minute old is fine and an hour old is a dead relay.
func TestHealthIsJudgedByTheLastHandshake(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    Health
		want bool
	}{
		{"interface missing", Health{}, false},
		{"up but never handshaken", Health{Up: true}, false},
		{"handshaken a minute ago", Health{Up: true, HandshakeAge: time.Minute}, true},
		{"handshaken an hour ago", Health{Up: true, HandshakeAge: time.Hour}, false},
	} {
		if got := tc.h.Healthy(); got != tc.want {
			t.Errorf("%s: healthy=%v, wanted %v", tc.name, got, tc.want)
		}
	}
}

// Dial has to refuse rather than reach out with nothing to prove who it is.
func TestDialRefusesWithNoCredentialAtAll(t *testing.T) {
	_, err := Dial(t.Context(), Credentials{Address: "198.51.100.1", Port: 22, User: "root"})
	if err == nil {
		t.Fatal("dialled with neither a password nor a key")
	}
	if !strings.Contains(err.Error(), "no password and no key") {
		t.Fatalf("the reason was not the missing credential: %v", err)
	}
}

// An empty user means root, because that is what the operator left the field
// as, and an empty port means 22 for the same reason.
func TestTheDefaultsAreTheOnesAnOperatorExpects(t *testing.T) {
	c := Credentials{Address: "198.51.100.1"}
	if c.user() != "root" {
		t.Errorf("empty user became %q", c.user())
	}
	if c.addr() != "198.51.100.1:22" {
		t.Errorf("empty port became %q", c.addr())
	}
}

// A flag is a country's two letters written as regional indicators, so there
// is no table to keep up to date and no country that can be missing from one.
// Measured on ocserv 1.2.4: a group label carrying one reaches an AnyConnect
// client byte for byte, and the real Cisco client renders it.
func TestAFlagIsBuiltFromTheCountryCode(t *testing.T) {
	for code, want := range map[string]string{
		"NL":   "\U0001F1F3\U0001F1F1",
		"FR":   "\U0001F1EB\U0001F1F7",
		"de":   "\U0001F1E9\U0001F1EA",
		" us ": "\U0001F1FA\U0001F1F8",
	} {
		if got := FlagFor(code); got != want {
			t.Errorf("%q gave %q, wanted %q", code, got, want)
		}
	}
	for _, bad := range []string{"", "N", "NLD", "N1", "??"} {
		if got := FlagFor(bad); got != "" {
			t.Errorf("%q should have no flag, got %q", bad, got)
		}
	}
}

// The label is what a customer reads. A lookup that half failed must still
// leave something readable rather than an empty entry in their dropdown.
func TestTheLabelSurvivesAHalfFailedLookup(t *testing.T) {
	if got := LabelFor("Netherlands", "NL"); got != "\U0001F1F3\U0001F1F1 Netherlands" {
		t.Errorf("full lookup gave %q", got)
	}
	if got := LabelFor("Netherlands", ""); got != "Netherlands" {
		t.Errorf("no code gave %q", got)
	}
	if got := LabelFor("", "NL"); got != "\U0001F1F3\U0001F1F1" {
		t.Errorf("no name gave %q", got)
	}
	if got := LabelFor("", ""); got != "" {
		t.Errorf("nothing at all gave %q", got)
	}
}
