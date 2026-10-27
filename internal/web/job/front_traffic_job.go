package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// FrontTrafficJob adds what each BackPack entry server carried since the last
// tick to its totals; with no such front it costs one query.
type FrontTrafficJob struct {
	relayService service.RelayService
}

func NewFrontTrafficJob() *FrontTrafficJob { return new(FrontTrafficJob) }

func (j *FrontTrafficJob) Run() { j.relayService.CollectFrontTraffic() }
