package job

import (
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
)

// AdminLimitsJob switches off resellers whose expiry has passed or whose
// traffic quota is used up, and takes their created inbounds down with them.
type AdminLimitsJob struct {
	adminService   panel.AdminService
	inboundService service.InboundService
	xrayService    service.XrayService
}

func NewAdminLimitsJob() *AdminLimitsJob {
	return new(AdminLimitsJob)
}

func (j *AdminLimitsJob) Run() {
	breaches, err := j.adminService.Breaches(time.Now().UnixMilli())
	if err != nil {
		logger.Warning("admin limits: listing breaches failed:", err)
		return
	}
	needRestart := false
	for _, b := range breaches {
		nr, err := j.adminService.Deactivate(&j.inboundService, b.AdminId, b.Reason)
		if err != nil {
			logger.Warning("admin limits: deactivating", b.Username, "failed:", err)
		}
		logger.Infof("admin limits: %s disabled (%s)", b.Username, b.Reason)
		needRestart = needRestart || nr
	}
	if needRestart {
		j.xrayService.SetToNeedRestart()
	}
}
