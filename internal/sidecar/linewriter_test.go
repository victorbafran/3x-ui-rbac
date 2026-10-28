package sidecar

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Lines captured from the panel box's log (2026-10-26); the tunnel ones carry
// BackPack's colour codes exactly as they reached the panel.
const (
	unterminated  = "ocserv[3305684]: GnuTLS error (at worker-vpn.c:888): The TLS connection was non-properly terminated."
	undecodable   = "ocserv[3305694]: GnuTLS error (at worker-vpn.c:888): Error decoding the received TLS packet."
	timedOut      = "ocserv[3305688]: GnuTLS error (at worker-vpn.c:888): The operation timed out"
	certIsBad     = "ocserv[3306464]: worker: 127.0.0.1 TLS alert (at worker-vpn.c:887): Certificate is bad"
	acmeHead      = "26-Oct 17:47:31 [\x1b[31mERROR\x1b[0m] nothing is listening on 127.0.0.1:80 on THIS server."
	forwardedHead = "26-Oct 17:26:02 [\x1b[31mERROR\x1b[0m] nothing is listening on 127.0.0.1:45222 on THIS server."
)

var explanation = []string{
	"The tunnel delivered the connection correctly — the problem is the",
	"service being forwarded to. Either it is not running, or it is bound",
	"to a different address (many panels bind a public IP, not 127.0.0.1).",
}

func tail(port int) []string {
	return append(slices.Clone(explanation), "Check with:  ss -tlnp | grep "+strconv.Itoa(port), "(repeats are suppressed for 30s)")
}

var handshakeNoise = []NoiseRule{
	{Kind: "non-properly terminated", Line: regexp.MustCompile(`GnuTLS error \(at worker-vpn\.c:\d+\): The TLS connection was non-properly terminated\.$`)},
	{Kind: "error decoding", Line: regexp.MustCompile(`GnuTLS error \(at worker-vpn\.c:\d+\): Error decoding the received TLS packet\.$`)},
}

var acmeNoise = []NoiseRule{{
	Kind:         "nothing on :80",
	Line:         regexp.MustCompile(`\[ERROR\] nothing is listening on 127\.0\.0\.1:80 on THIS server\.$`),
	Continuation: regexp.MustCompile(`^(The tunnel delivered|service being forwarded to\.|to a different address|Check with:|\(repeats are suppressed)`),
}}

// runs keeps labels apart across -count: the panel log buffer outlives a test.
var runs atomic.Int64

// writerAt is a writer whose clock the test moves by hand.
func writerAt(t *testing.T, noise []NoiseRule) (w *LineWriter, label string, advance func(time.Duration)) {
	t.Helper()
	label = fmt.Sprintf("test %s %d", t.Name(), runs.Add(1))
	now := time.Date(2026, 10, 26, 17, 0, 0, 0, time.UTC)
	w = NewLineWriter(label, noise...)
	w.now = func() time.Time { return now }
	return w, label, func(d time.Duration) { now = now.Add(d) }
}

func write(t *testing.T, w *LineWriter, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if _, err := w.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write %q: %v", line, err)
		}
	}
}

// logged is what reached the panel log under label, oldest first, each
// without its time and level.
func logged(label string) []string {
	var out []string
	for _, line := range logger.GetLogs(10000, "debug") {
		if _, msg, ok := strings.Cut(line, " - "); ok && strings.HasPrefix(msg, label+" | ") {
			out = append(out, strings.TrimPrefix(msg, label+" | "))
		}
	}
	slices.Reverse(out)
	return out
}

func sameLines(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("panel log:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// 76% of the panel's log buffer was these two, which pushed every real line
// out within hours. They must become one count line per window, not one each.
func TestHandshakeNoiseIsCountedOncePerWindow(t *testing.T) {
	w, label, advance := writerAt(t, handshakeNoise)

	write(t, w, unterminated, undecodable, timedOut, unterminated)
	advance(4 * time.Minute)
	write(t, w, certIsBad, undecodable, unterminated)
	sameLines(t, logged(label), []string{timedOut, certIsBad})

	advance(6 * time.Minute)
	write(t, w, timedOut)
	sameLines(t, logged(label), []string{
		timedOut,
		certIsBad,
		"suppressed 5 probe messages in 10m: non-properly terminated 3, error decoding 2",
		timedOut,
	})
}

// The panel shows the last line beside an instance that will not start; a
// scanner's knock there would hide the reason it stopped.
func TestLastLineSkipsNoise(t *testing.T) {
	w, _, _ := writerAt(t, handshakeNoise)
	write(t, w, certIsBad, unterminated, undecodable)
	if got := w.LastLine(); got != certIsBad {
		t.Fatalf("LastLine = %q, want the last real line %q", got, certIsBad)
	}
}

// A process that exits mid-window must not take its count with it.
func TestFlushReportsThePendingCount(t *testing.T) {
	w, label, advance := writerAt(t, handshakeNoise)
	write(t, w, undecodable, undecodable)
	advance(3 * time.Minute)
	w.Flush()
	sameLines(t, logged(label), []string{"suppressed 2 probe messages in 3m: error decoding 2"})
}

// Psiphon's state lives in lines folded out of the log, so the callback sees
// those too, a last line cut off by the exit, and may read the writer back.
func TestOnLineSeesFoldedLinesAndMayReadTheWriter(t *testing.T) {
	w, label, _ := writerAt(t, handshakeNoise)
	var got []string
	w.setOnLine(func(line string) {
		_ = w.LastLine()
		got = append(got, line)
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = w.Write([]byte(certIsBad + "\n  \n" + unterminated + "\n" + timedOut))
		w.Flush()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a callback reading the writer back wedged it")
	}
	sameLines(t, got, []string{certIsBad, unterminated, timedOut})
	sameLines(t, logged(label), []string{certIsBad, timedOut, "suppressed 1 probe messages in 0s: non-properly terminated 1"})
}

// A folded error takes its five explanation lines with it; the same block for
// another port stays whole and uncoloured, as there an inbound is down.
func TestAFoldedErrorTakesItsExplanationAndNothingElse(t *testing.T) {
	w, label, _ := writerAt(t, acmeNoise)
	write(t, w, acmeHead)
	write(t, w, tail(80)...)
	write(t, w, forwardedHead)
	write(t, w, tail(45222)...)
	write(t, w, acmeHead)
	write(t, w, tail(80)...)
	w.Flush()

	want := append([]string{"26-Oct 17:26:02 [ERROR] nothing is listening on 127.0.0.1:45222 on THIS server."}, tail(45222)...)
	want = append(want, "suppressed 2 probe messages in 0s: nothing on :80 2")
	sameLines(t, logged(label), want)
	if got := w.LastLine(); got != "(repeats are suppressed for 30s)" {
		t.Fatalf("LastLine = %q, want the kept block's last line", got)
	}
}
