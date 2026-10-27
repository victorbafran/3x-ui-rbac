package ocserv

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Each folded kind once, as ocserv -d 1 printed it on the panel box
// (2026-10-26); together they were 84% of everything x-ui logged.
var scannerHandshakes = []string{
	"ocserv[3305684]: GnuTLS error (at worker-vpn.c:888): The TLS connection was non-properly terminated.",
	"ocserv[3305694]: GnuTLS error (at worker-vpn.c:888): Error decoding the received TLS packet.",
	"ocserv[3306273]: GnuTLS error (at worker-vpn.c:888): A packet with illegal or unsupported version was received.",
	"ocserv[3305940]: GnuTLS error (at worker-vpn.c:888): An unexpected TLS packet was received.",
	"ocserv[3306268]: GnuTLS error (at worker-vpn.c:888): No supported cipher suites have been found.",
	"ocserv[3306261]: GnuTLS error (at worker-vpn.c:888): Error in the pull function.",
	"ocserv[3305940]: worker: 127.0.0.1 warning: Received record packet of unknown type 18",
}

// What tells DPI, a wrong certificate pin or a broken sandbox apart, and a
// session's own error (worker[id], from the box's journal copy).
var diagnoses = []string{
	"ocserv[3305688]: GnuTLS error (at worker-vpn.c:888): The operation timed out",
	"ocserv[3306464]: GnuTLS error (at worker-vpn.c:888): A TLS fatal alert has been received.",
	"ocserv[3306464]: worker: 127.0.0.1 TLS alert (at worker-vpn.c:887): Certificate is bad",
	"ocserv[3306464]: GnuTLS error (at worker-vpn.c:888): GnuTLS internal error.",
	"ocserv[3305517]: worker[9tlla6k2qd]: 127.0.0.1 GnuTLS error (at worker-vpn.c:1571): Error decoding the received TLS packet.",
}

// noiseRuns keeps labels apart across -count: the panel log buffer outlives a test.
var noiseRuns atomic.Int64

func loggedUnder(label string) []string {
	var out []string
	for _, line := range logger.GetLogs(10000, "debug") {
		if _, msg, ok := strings.Cut(line, " - "); ok && strings.HasPrefix(msg, label+" | ") {
			out = append(out, strings.TrimPrefix(msg, label+" | "))
		}
	}
	slices.Reverse(out)
	return out
}

func TestScannerHandshakesAreFoldedAndDiagnosesKept(t *testing.T) {
	label := fmt.Sprintf("in-%s-%d", t.Name(), noiseRuns.Add(1))
	p := newProcess(configPathForID(7), label)
	for i, line := range diagnoses {
		for _, out := range []string{line, scannerHandshakes[i%len(scannerHandshakes)]} {
			if _, err := p.logWriter.Write([]byte(out + "\n")); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
	}
	for _, line := range scannerHandshakes {
		if _, err := p.logWriter.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got, want := p.GetResult(), diagnoses[len(diagnoses)-1]; got != want {
		t.Fatalf("GetResult = %q, want the last real line %q", got, want)
	}
	p.logWriter.Flush()

	got := loggedUnder("ocserv: " + label)
	if len(got) != len(diagnoses)+1 || !slices.Equal(got[:len(diagnoses)], diagnoses) {
		t.Fatalf("panel log:\n  %s\nwant every diagnosis word for word, then one count line", strings.Join(got, "\n  "))
	}
	count := regexp.MustCompile(`^suppressed 12 probe messages in \d+s: non-properly terminated 2, error decoding 2, unsupported version 2, unexpected packet 2, no cipher suites 2, pull function 1, unknown record type 1$`)
	if !count.MatchString(got[len(got)-1]) {
		t.Fatalf("count line = %q", got[len(got)-1])
	}
}
