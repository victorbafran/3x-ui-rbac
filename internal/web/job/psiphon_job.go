package job

import (
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// PsiphonJob keeps one client up per enabled Psiphon country, restarting any
// that died, and checks where each leaves from; with none enabled it does nothing.
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
	if err := mgr.Reconcile(wanted); err != nil {
		logger.Warning("psiphon job:", err)
	}
	j.psiphonService.CheckExits()
}
