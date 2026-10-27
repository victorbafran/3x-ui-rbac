package tunnel

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// refusal is BackPack's block for a port nothing answers on, as tunnel 18
// printed it on the panel box (2026-10-26), colour codes included.
func refusal(port string) []string {
	return []string{
		"26-Oct 17:47:31 [\x1b[31mERROR\x1b[0m] nothing is listening on 127.0.0.1:" + port + " on THIS server.",
		"The tunnel delivered the connection correctly — the problem is the",
		"service being forwarded to. Either it is not running, or it is bound",
		"to a different address (many panels bind a public IP, not 127.0.0.1).",
		"Check with:  ss -tlnp | grep " + port,
		"(repeats are suppressed for 30s)",
	}
}

// acmeNoiseRuns keeps labels apart across -count: the panel log buffer outlives a test.
var acmeNoiseRuns atomic.Int64

// Scanners hit the unanswered ACME port 232 times a day; the same block for an
// inbound's port means customers are being refused, so it must stay.
func TestOnlyTheACMEPortRefusalIsFolded(t *testing.T) {
	label := fmt.Sprintf("tunnel %s-%d", t.Name(), acmeNoiseRuns.Add(1))
	w := sidecar.NewLineWriter(label, acmePortNoise...)
	dialFailure := "27-Oct 13:26:36 [\x1b[31mERROR\x1b[0m] channel dialer: dial tcp <nil>->37.152.191.4:3080: i/o timeout"
	var out []string
	out = append(out, refusal("80")...)
	out = append(out, refusal("45222")...)
	out = append(out, refusal("80")...)
	out = append(out, dialFailure)
	for _, line := range out {
		if _, err := w.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	w.Flush()

	var got []string
	for _, line := range logger.GetLogs(10000, "debug") {
		if _, msg, ok := strings.Cut(line, " - "); ok && strings.HasPrefix(msg, label+" | ") {
			got = append(got, strings.TrimPrefix(msg, label+" | "))
		}
	}
	slices.Reverse(got)
	want := []string{"26-Oct 17:47:31 [ERROR] nothing is listening on 127.0.0.1:45222 on THIS server."}
	want = append(want, refusal("45222")[1:]...)
	want = append(want, "27-Oct 13:26:36 [ERROR] channel dialer: dial tcp <nil>->37.152.191.4:3080: i/o timeout")
	if len(got) != len(want)+1 || !slices.Equal(got[:len(want)], want) {
		t.Fatalf("panel log:\n  %s\nwant:\n  %s\n  then one count line", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if count := regexp.MustCompile(`^suppressed 2 probe messages in \d+s: nothing on :80 2$`); !count.MatchString(got[len(got)-1]) {
		t.Fatalf("count line = %q", got[len(got)-1])
	}
}
