package job

import (
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// ArvanJob checks each ArvanCloud front's active IP, swaps when asked to and keeps
// the reserves tested; with no bound front it costs one query a minute.
type ArvanJob struct {
	arvanService service.ArvanService
}

func NewArvanJob() *ArvanJob { return new(ArvanJob) }

func (j *ArvanJob) Run() { j.arvanService.Watch() }
