package job

import (
	"errors"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// PsiphonJob saves the countries' traffic and keeps one client up per enabled country, fetching
// the client where it is missing and restarting any that died, and checks where each leaves from.
type PsiphonJob struct {
	psiphonService service.PsiphonService
	// busy skips a tick while the last one still runs: a first start fetches
	// the client, and a round of exit checks can take twenty seconds.
	busy sync.Mutex
}

func NewPsiphonJob() *PsiphonJob { return new(PsiphonJob) }

func (j *PsiphonJob) Run() {
	if !j.busy.TryLock() {
		return
	}
	defer j.busy.Unlock()
	// First, and with no country enabled too: one just switched off still
	// has bytes waiting.
	if err := j.psiphonService.FlushTraffic(); err != nil {
		logger.Warning("psiphon job: could not save the traffic:", err)
	}
	wanted, err := j.psiphonService.Wanted()
	if err != nil {
		logger.Warning("psiphon job:", err)
		return
	}
	mgr := psiphon.GetManager()
	if len(wanted) == 0 {
		if mgr.HasRunning() {
			if err := mgr.Reconcile(nil); err != nil {
				logger.Warning("psiphon job:", err)
			}
		}
		return
	}
	// A download that failed was logged when it did; the ticks until the next try say nothing new.
	if err := mgr.Reconcile(wanted); err != nil && !errors.Is(err, psiphon.ErrFetchWaiting) {
		logger.Warning("psiphon job:", err)
	}
	j.psiphonService.CheckExits()
}
