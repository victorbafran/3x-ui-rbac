package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// FrontTrafficJob adds what each entry server carried since the last tick to its
// totals; with no front it costs one query, with BackPack fronts alone no iptables.
type FrontTrafficJob struct {
	relayService service.RelayService
}

func NewFrontTrafficJob() *FrontTrafficJob { return new(FrontTrafficJob) }

func (j *FrontTrafficJob) Run() { j.relayService.CollectFrontTraffic() }
