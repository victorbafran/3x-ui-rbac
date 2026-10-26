package job

import (
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// The live speed column in the clients list is fed by a websocket push, not by
// the stored totals. A protocol that saves its traffic to the database and
// never pushes shows the right usage and a blank speed — which reads as a
// customer who is not connected, on a customer who plainly is.
//
// xray and tuic pushed; cisco and OpenVPN did not, because each one's job was
// written from the shape of the job beside it and that neighbour happened to
// be one of the two that did not. So the push lives here once, and a protocol
// served by its own sidecar gets it by calling this rather than by someone
// remembering.

// defaultSidecarSpeedSampleInterval is the fallback when there is no previous
// sample to measure against — the first run after a restart, and nothing else.
const defaultSidecarSpeedSampleInterval = 10 * time.Second

// speedSampleInterval is how long the deltas being pushed accumulated over.
//
// Measured between runs rather than assumed from the cron cadence: a job that
// was slow, or that skipped a tick because the host was busy, has collected
// more than one interval's worth, and dividing by the cadence would report a
// speed several times what the customer actually got.
func speedSampleInterval(previous, current time.Time) time.Duration {
	if previous.IsZero() || !current.After(previous) {
		return defaultSidecarSpeedSampleInterval
	}
	return current.Sub(previous)
}

// sidecarSpeedPayload is the shape the clients page already understands. The
// source names the protocol so the page can keep each one's numbers apart and
// expire them separately: a sidecar that stops pushing should drop its own
// speeds to zero without touching anybody else's.
func sidecarSpeedPayload(source string, clientTraffics []*xray.ClientTraffic, sampleInterval time.Duration) trafficPush {
	intervalMs := sampleInterval.Milliseconds()
	if intervalMs < 1 {
		intervalMs = 1
	}
	return trafficPush{
		"clientTraffics":          clientTraffics,
		"clientTrafficSource":     source,
		"clientTrafficIntervalMs": intervalMs,
	}
}

// broadcastSidecarSpeed pushes one sample. Called only after the traffic has
// been persisted: a speed the page shows for traffic the database rejected is
// a number that disagrees with the total beside it.
func broadcastSidecarSpeed(source string, clientTraffics []*xray.ClientTraffic, sampleInterval time.Duration) {
	if len(clientTraffics) == 0 {
		return
	}
	websocket.BroadcastTraffic(sidecarSpeedPayload(source, clientTraffics, sampleInterval), nil)
}
