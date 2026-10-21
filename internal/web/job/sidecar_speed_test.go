package job

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// The gap this file exists for: cisco and OpenVPN saved their traffic and
// never pushed a speed sample, so a connected customer showed the right total
// usage beside a blank speed — which reads as somebody who is not connected.
//
// Each job was written from the shape of the job next to it, and the
// neighbour happened to be one that did not push. Nothing about either job in
// isolation looked wrong.

// Every protocol served by its own sidecar has to name itself, and the names
// have to be the ones the clients page keeps separate buckets for. A sample
// pushed under a name the page does not know is dropped into xray's bucket
// and expires with xray's samples.
func TestEverySidecarNamesItselfWithANameThePageKnows(t *testing.T) {
	// The union in frontend/src/hooks/useClients.ts. A protocol added here
	// without being added there shows no speed at all.
	known := map[string]bool{"xray": true, "tuic": true, "cisco": true, "openvpn": true}
	for _, source := range []string{"cisco", "openvpn"} {
		if !known[source] {
			t.Errorf("%q is not one of the sources the clients page buckets by", source)
		}
		payload := sidecarSpeedPayload(source, []*xray.ClientTraffic{{Email: "a@b", Up: 1, Down: 2}}, time.Second)
		if payload["clientTrafficSource"] != source {
			t.Errorf("payload says %v, want %q", payload["clientTrafficSource"], source)
		}
	}
}

// The page divides bytes by this to get a rate, so it has to be the time the
// bytes actually accumulated over.
func TestTheIntervalIsMeasuredNotAssumed(t *testing.T) {
	base := time.Unix(1792572428, 0)

	// A job that skipped a tick collected two intervals' worth. Reporting the
	// cadence instead would show a customer twice the speed they got.
	if got := speedSampleInterval(base, base.Add(20*time.Second)); got != 20*time.Second {
		t.Errorf("interval = %v, want the 20s that actually elapsed", got)
	}

	// First run after a restart: there is nothing to measure against.
	if got := speedSampleInterval(time.Time{}, base); got != defaultSidecarSpeedSampleInterval {
		t.Errorf("interval = %v, want the default with no previous sample", got)
	}

	// A clock that went backwards must not produce a negative or zero
	// interval, which the page would divide by.
	if got := speedSampleInterval(base, base.Add(-5*time.Second)); got != defaultSidecarSpeedSampleInterval {
		t.Errorf("interval = %v, want the default when the clock moved backwards", got)
	}
	if got := speedSampleInterval(base, base); got != defaultSidecarSpeedSampleInterval {
		t.Errorf("interval = %v, want the default when no time passed", got)
	}
}

// A sub-millisecond interval would divide into a speed of billions. It cannot
// happen through speedSampleInterval, but the payload is the last thing
// between a bad duration and the customer's screen.
func TestAnImpossiblyShortIntervalIsNotDividedBy(t *testing.T) {
	payload := sidecarSpeedPayload("openvpn", []*xray.ClientTraffic{{Email: "a@b"}}, time.Microsecond)
	if ms, ok := payload["clientTrafficIntervalMs"].(int64); !ok || ms < 1 {
		t.Fatalf("interval sent as %v; the page divides by this", payload["clientTrafficIntervalMs"])
	}
}

// Nothing moved, nothing to say. A push of an empty list would reset every
// speed on screen to zero once per tick.
func TestNothingIsPushedWhenNobodyMoved(t *testing.T) {
	// broadcastSidecarSpeed reaches the websocket hub, which is not running
	// in a unit test; the guard is what keeps it from being reached at all.
	broadcastSidecarSpeed("openvpn", nil, time.Second)
	broadcastSidecarSpeed("cisco", []*xray.ClientTraffic{}, time.Second)
}
