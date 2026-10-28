package ocserv

import (
	"context"
	"os/exec"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// ocserv hands a client an address out of the instance pool and routes its
// traffic onto the host, but it does not translate that traffic onto the host's
// own address. Without the rules below a client connects, gets a lease, and
// then reaches nothing at all — which reads as "the VPN is broken" rather than
// "the host is not forwarding".

const natTimeout = 10 * time.Second

var run = func(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), natTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}

func ruleExists(args ...string) bool {
	check := append([]string{"-t", "nat", "-C", "POSTROUTING"}, args...)
	return run("iptables", check...) == nil
}

// ensureNAT turns on forwarding and masquerades the instance's pool. It is
// idempotent: the rule is added only when an identical one is not already
// there, so repeated reconciles do not stack duplicates.
func ensureNAT(cidr string) {
	if !onLinux || cidr == "" {
		return
	}
	if err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		logger.Warningf("ocserv: could not enable ip_forward: %v", err)
	}
	rule := []string{"-s", cidr, "-j", "MASQUERADE"}
	if ruleExists(rule...) {
		return
	}
	add := append([]string{"-t", "nat", "-A", "POSTROUTING"}, rule...)
	if err := run("iptables", add...); err != nil {
		logger.Warningf("ocserv: could not add the NAT rule for %s (clients will connect but reach nothing): %v", cidr, err)
		return
	}
	logger.Infof("ocserv: NAT enabled for %s", cidr)
}

// removeNAT drops the masquerade rule for a pool that is no longer served, so
// a deleted inbound does not leave a rule behind for the next one to inherit.
func removeNAT(cidr string) {
	if !onLinux || cidr == "" {
		return
	}
	rule := []string{"-s", cidr, "-j", "MASQUERADE"}
	for ruleExists(rule...) {
		del := append([]string{"-t", "nat", "-D", "POSTROUTING"}, rule...)
		if err := run("iptables", del...); err != nil {
			return
		}
	}
}
