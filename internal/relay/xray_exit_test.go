package relay

import (
	"slices"
	"strings"
	"testing"
)

// The source must sit inside exitPools, or the exit's WireGuard peer drops
// every packet: its AllowedIPs and masquerade cover that block and no other.
func TestAnXrayExitSendsFromInsideTheExitsPools(t *testing.T) {
	for id, want := range map[int]string{1: "10.255.255.1", 254: "10.255.255.254"} {
		got, err := XrayExitSource(id)
		if err != nil || got != want {
			t.Errorf("XrayExitSource(%d) = %q, %v; want %q", id, got, err, want)
		}
	}
	for _, id := range []int{0, 255} {
		if got, err := XrayExitSource(id); err == nil {
			t.Errorf("XrayExitSource(%d) = %q; an address outside .1-.254 was handed out", id, got)
		}
	}
}

func TestAnInterfaceNameGivesBackItsRelay(t *testing.T) {
	if id, ok := InterfaceID("wgr9"); !ok || id != 9 {
		t.Errorf("InterfaceID(wgr9) = %d, %v", id, ok)
	}
	for _, name := range []string{"", "wgr", "wgr0", "wgr09", "wg9", "eth0", "wgr9x"} {
		if id, ok := InterfaceID(name); ok {
			t.Errorf("InterfaceID(%q) = %d; it is not a name InterfaceName makes", name, id)
		}
	}
}

// The rule comes before main's 32766, or main's default route answers first
// and the traffic leaves this host instead of the exit.
func TestTheExitsRuleSteersItsSourceIntoItsOwnTable(t *testing.T) {
	cmds, err := xrayExitCommands(9, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cmds {
		got = append(got, strings.Join(c, " "))
	}
	want := []string{
		"ip addr replace 10.255.255.9/32 dev wgr9",
		"ip route replace default dev wgr9 table 60009",
		"ip rule add from 10.255.255.9 lookup 60009 priority 6009",
	}
	if !slices.Equal(got, want) {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}

	again, _ := xrayExitCommands(9, false)
	if len(again) != 2 {
		t.Errorf("a rule already in place was added again: %v", again)
	}
}

// Read from `ip rule show`, so a reconcile does not stack a copy each minute;
// exit 1's rule must not be mistaken for exit 12's.
func TestTheExitsRuleIsRecognisedOnlyAsItself(t *testing.T) {
	out := "0:\tfrom all lookup local\n6012:\tfrom 10.255.255.12 lookup 60012\n32766:\tfrom all lookup main\n"
	if !xrayExitRuleIn(out, 12) {
		t.Error("exit 12's own rule was not recognised")
	}
	if xrayExitRuleIn(out, 1) {
		t.Error("exit 12's rule was taken for exit 1's")
	}
	// Someone else's rule at exit 1's priority is not exit 1's rule, or it
	// would never be added.
	for _, foreign := range []string{"6001:\tfrom 10.8.0.0/24 lookup 100\n", "6001:\tfrom 10.255.255.12 lookup 60012\n"} {
		if xrayExitRuleIn(foreign, 1) {
			t.Errorf("%q was taken for exit 1's rule", foreign)
		}
	}
}
