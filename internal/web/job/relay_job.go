package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// RelayJob brings the tunnel to each exit country back when it is missing.
//
// A WireGuard interface does not survive a reboot, while everything it needs —
// the keys, the endpoint, the relay's own configuration — does. Without this
// the panel would come back up offering countries whose traffic goes nowhere,
// and the only symptom a customer reports is "the Netherlands one is broken".
//
// It does nothing at all while every tunnel is healthy, so a panel with no
// relays pays only one database query a minute.
type RelayJob struct {
	relayService service.RelayService
}

func NewRelayJob() *RelayJob { return new(RelayJob) }

func (j *RelayJob) Run() { j.relayService.Reconcile() }
