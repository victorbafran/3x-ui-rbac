package relay

import (
	"slices"
	"strings"
	"testing"
)

// Measured on the UK exit: its peer allows 10.248.0.0/13 and masquerades only
// that, so OpenVPN's 10.1.3.0/24 connected and reached nothing.
func TestOnlyAPoolInsideTheExitsBlockGoesInAsItIs(t *testing.T) {
	for cidr, inside := range map[string]bool{
		"10.248.16.0/24":  true,
		"10.255.255.0/24": true,
		"10.1.3.0/24":     false,
		"10.8.1.0/24":     false,
		"10.0.0.0/8":      false, // outside the block entirely
		"not a cidr":      false,
	} {
		if got := PoolInsideExitPools(cidr); got != inside {
			t.Errorf("PoolInsideExitPools(%q) = %v, want %v", cidr, got, inside)
		}
	}
}

// OpenVPN's 10.1.x pool on a Psiphon exit was refused as "not one of this panel's
// exits" and logged on every start; Xray's TUN takes any source as it is. A rewrite
// the pool kept from a relay it left goes, or its packets reach Xray as the relay's.
func TestAPsiphonExitNeedsNoRewrite(t *testing.T) {
	var ran []string
	ipt := func(args ...string) (string, error) {
		ran = append(ran, strings.Join(args, " "))
		if slices.Contains(args, "-S") {
			return "-P POSTROUTING ACCEPT\n-A POSTROUTING -s 10.1.3.0/24 -o wgr9 -j SNAT --to-source 10.255.255.9\n", nil
		}
		return "", nil
	}
	if err := ensureExitSNAT("10.1.3.0/24", "psi-AT", ipt); err != nil {
		t.Fatalf("a Psiphon exit: %v", err)
	}
	want := []string{
		"-t nat -S POSTROUTING",
		"-t nat -D POSTROUTING -s 10.1.3.0/24 -o wgr9 -j SNAT --to-source 10.255.255.9",
	}
	if !slices.Equal(ran, want) {
		t.Errorf("ran %q, want only the old relay rewrite taken out: %q", ran, want)
	}
	if !exitNeedsSNAT("10.1.3.0/24", "wgr9") {
		t.Error("a relay exit lost the rewrite its peer needs for a pool outside exitPools")
	}
}

func TestAPoolOutsideIsRewrittenToItsExitsSource(t *testing.T) {
	got, err := exitSNATArgs("-A", "10.1.3.0/24", "wgr9")
	if err != nil {
		t.Fatal(err)
	}
	want := "-t nat -A POSTROUTING -s 10.1.3.0/24 -o wgr9 -j SNAT --to-source 10.255.255.9"
	if strings.Join(got, " ") != want {
		t.Errorf("rule %q, want %q", strings.Join(got, " "), want)
	}
	if _, err := exitSNATArgs("-A", "10.1.3.0/24", "tun0"); err == nil {
		t.Error("an interface that is not a panel exit got a source address")
	}
}

// Removal finds the pool's rewrite whichever exit it names, so moving an inbound
// to another exit leaves nothing behind, and touches no other rule.
func TestRemovalTakesOnlyThisPoolsRewrites(t *testing.T) {
	listing := strings.Join([]string{
		"-P POSTROUTING ACCEPT",
		"-A POSTROUTING -s 10.8.1.0/24 -j MASQUERADE",
		"-A POSTROUTING -s 10.1.3.0/24 -o wgr9 -j SNAT --to-source 10.255.255.9",
		"-A POSTROUTING -s 10.1.3.0/24 -o wgr12 -j SNAT --to-source 10.255.255.12",
		"-A POSTROUTING -s 10.1.30.0/24 -o wgr9 -j SNAT --to-source 10.255.255.9",
	}, "\n")
	got := exitSNATRulesFor(listing, "10.1.3.0/24")
	if len(got) != 2 {
		t.Fatalf("got %d rules to delete, want the two for 10.1.3.0/24: %q", len(got), got)
	}
	want := []string{"-t", "nat", "-D", "POSTROUTING", "-s", "10.1.3.0/24", "-o", "wgr9", "-j", "SNAT", "--to-source", "10.255.255.9"}
	if !slices.Equal(got[0], want) {
		t.Errorf("delete args %q, want %q", got[0], want)
	}
}
