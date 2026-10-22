package tunnel

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// Only addresses a front can drop reach the script; one it cannot parse would
// fail every ban beside it, and a private one could cut the front off itself.
func TestOnlyPublicAddressesAreCutOff(t *testing.T) {
	got := banTargets([]string{
		"5.22.109.78", "5.22.109.78", "::ffff:5.22.109.79", "2a01:5ec0::1",
		"127.0.0.1", "10.0.0.5", "192.168.1.2", "fe80::1", "0.0.0.0", "nonsense", "",
	})
	want := []string{"5.22.109.78", "5.22.109.79", "2a01:5ec0::1"}
	if !slices.Equal(got, want) {
		t.Errorf("targets %q, want %q", got, want)
	}
}

type banRemote struct {
	fakeRemote
	name string
	args []string
}

func (b *banRemote) RunScript(_ context.Context, name, _ string, args ...string) (string, error) {
	b.name, b.args = name, args
	return "banned", nil
}

// The script gets the SSH port to spare, the ban's length and the addresses.
func TestABanSparesSSHAndLastsItsTime(t *testing.T) {
	r := &banRemote{}
	if err := ban(context.Background(), r, 2222, []string{"5.22.109.78"}, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.args, " "); got != "2222 1800 5.22.109.78" {
		t.Errorf("args %q, want the SSH port, 1800 seconds and the address", got)
	}
	if err := ban(context.Background(), r, 0, []string{"5.22.109.78"}, 0); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.args, " "); got != "22 1 5.22.109.78" {
		t.Errorf("args %q, want SSH's default port and at least a second", got)
	}
}

// The script spares SSH before it drops anything, or an operator whose own
// address is banned loses the box.
func TestTheScriptSparesSSHBeforeDropping(t *testing.T) {
	ssh := strings.Index(banScript, "tcp dport $SSH_PORT accept")
	drop := strings.Index(banScript, "saddr @v4 drop")
	if ssh < 0 || drop < 0 || ssh > drop {
		t.Errorf("the SSH exception must come before the drop rules")
	}
}

// Every script a front runs must at least parse; a slip in one fails on the
// front only, long after the suite was green.
func TestEveryFrontScriptParses(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash here")
	}
	for name, body := range map[string]string{
		"backhaul": remoteScript, "backpack": backpackRemoteScript,
		"backhaul teardown": teardownScript, "backpack teardown": backpackTeardownScript,
		"ban": banScript,
	} {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("the %s script does not parse: %v\n%s", name, err, out)
		}
	}
}
