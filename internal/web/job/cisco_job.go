package job

import (
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/ocserv"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// CiscoJob reconciles the running ocserv sidecars against the enabled cisco
// inbounds in the database, restarts any that died, and folds the per-client
// traffic read through occtl into the usual client and inbound accounting.
type CiscoJob struct {
	inboundService service.InboundService
	// lastSpeedSample is when the previous push measured from, so the rate
	// is divided by the interval that actually elapsed.
	lastSpeedSample time.Time
}

// NewCiscoJob creates a new cisco reconcile/traffic job instance.
func NewCiscoJob() *CiscoJob { return new(CiscoJob) }

// Run reconciles desired cisco inbounds with running ocserv processes and
// records per-client traffic deltas and online status.
func (j *CiscoJob) Run() {
	desired, err := j.inboundService.DesiredCiscoInstances()
	if err != nil {
		logger.Warning("cisco job: get desired instances failed:", err)
		return
	}

	mgr := ocserv.GetManager()
	// Nothing is wanted and nothing is running: skip the reconcile entirely so
	// a host without ocserv installed does not log a failure every 10 seconds
	// for a feature it is not using.
	if len(desired) == 0 && !mgr.HasRunning() {
		return
	}

	activeTags := make([]string, 0, len(desired))
	for _, inst := range desired {
		activeTags = append(activeTags, inst.Tag)
	}

	mgr.Reconcile(desired)

	deltas, onlineEmails := mgr.CollectTraffic()

	clientTraffics := make([]*xray.ClientTraffic, 0, len(deltas))
	inboundUp := make(map[string]int64)
	inboundDown := make(map[string]int64)
	for _, d := range deltas {
		clientTraffics = append(clientTraffics, &xray.ClientTraffic{
			Email: d.Email,
			Up:    d.Up,
			Down:  d.Down,
		})
		inboundUp[d.Tag] += d.Up
		inboundDown[d.Tag] += d.Down
	}

	traffics := make([]*xray.Traffic, 0, len(inboundUp))
	for tag, up := range inboundUp {
		traffics = append(traffics, &xray.Traffic{
			IsInbound: true,
			Tag:       tag,
			Up:        up,
			Down:      inboundDown[tag],
		})
	}

	sampledAt := time.Now()
	sampleInterval := speedSampleInterval(j.lastSpeedSample, sampledAt)
	persisted := true
	if len(traffics) > 0 || len(clientTraffics) > 0 {
		if _, _, err := j.inboundService.AddTraffic(traffics, clientTraffics); err != nil {
			logger.Warning("cisco job: add traffic failed:", err)
			persisted = false
		}
	}
	// Only after it is stored: a speed shown for traffic the database refused
	// is a number that disagrees with the total printed beside it.
	if persisted {
		broadcastSidecarSpeed("cisco", clientTraffics, sampleInterval)
		j.lastSpeedSample = sampledAt
	}

	j.inboundService.RefreshLocalOnlineClients(onlineEmails, activeTags)
}
