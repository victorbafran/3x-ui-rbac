package job

import (
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/openvpn"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// OpenVPNJob reconciles the running sidecars against the enabled OpenVPN
// inbounds, restarts any that died, and folds the per-client traffic read out
// of each server's own status file into the usual accounting.
//
// It mirrors CiscoJob deliberately: the two protocols must agree on what an
// active client is and on when traffic is counted, or an operator's totals
// disagree with themselves depending on which protocol a customer is on.
type OpenVPNJob struct {
	inboundService service.InboundService
	// lastSpeedSample is when the previous push measured from, so the rate
	// is divided by the interval that actually elapsed.
	lastSpeedSample time.Time
}

// openvpnSidecars is what a tick asks of the openvpn manager.
type openvpnSidecars interface {
	HasRunning() bool
	Reconcile([]openvpn.Instance)
	ReassertRoutes()
	CollectTraffic() ([]openvpn.Traffic, []string)
}

// openvpnManager is the openvpn manager; a variable so a test can see what a tick does.
var openvpnManager = func() openvpnSidecars { return openvpn.GetManager() }

func NewOpenVPNJob() *OpenVPNJob { return new(OpenVPNJob) }

func (j *OpenVPNJob) Run() {
	desired, err := j.inboundService.DesiredOpenVPNInstances()
	if err != nil {
		logger.Warning("openvpn job: get desired instances failed:", err)
		return
	}

	mgr := openvpnManager()
	// Nothing wanted and nothing running: skip entirely, so a host without
	// openvpn installed does not log a failure every few seconds for a
	// feature it is not using.
	if len(desired) == 0 && !mgr.HasRunning() {
		return
	}

	activeTags := make([]string, 0, len(desired))
	for _, inst := range desired {
		activeTags = append(activeTags, inst.Tag)
	}

	mgr.Reconcile(desired)
	// A relay brought back up took its route with it, a flushed iptables a Psiphon
	// redirect; nothing restarts the server to put them back.
	mgr.ReassertRoutes()

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
			logger.Warning("openvpn job: add traffic failed:", err)
			persisted = false
		}
	}
	// Only after it is stored: a speed shown for traffic the database refused
	// is a number that disagrees with the total printed beside it.
	if persisted {
		broadcastSidecarSpeed("openvpn", clientTraffics, sampleInterval)
		j.lastSpeedSample = sampledAt
	}

	j.inboundService.RefreshLocalOnlineClients(onlineEmails, activeTags)
}
