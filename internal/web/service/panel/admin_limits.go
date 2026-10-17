package panel

import (
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// Why an admin was switched off. Stored in users.disabled_reason so the
// admins page can say so; "" means switched off by hand, or not at all.
const (
	ReasonManual        = "manual"
	ReasonQuotaExceeded = "quota_exceeded"
	ReasonExpired       = "expired"
)

// LimitBreach names an admin who has run past a limit the super_admin set.
type LimitBreach struct {
	AdminId  int
	Username string
	Reason   string
}

// Breaches lists the enabled non-super admins whose expiry has passed or whose
// owned inbounds have used their whole traffic quota. A zero limit is no limit.
func (s *AdminService) Breaches(nowMs int64) ([]LimitBreach, error) {
	db := database.GetDB()
	var admins []model.User
	err := db.Where("role <> ? AND enabled = ? AND (traffic_quota > 0 OR expire_at > 0)",
		string(model.RoleSuperAdmin), true).Order("id ASC").Find(&admins).Error
	if err != nil {
		return nil, err
	}
	var out []LimitBreach
	for _, a := range admins {
		if a.ExpireAt > 0 && a.ExpireAt <= nowMs {
			out = append(out, LimitBreach{AdminId: a.Id, Username: a.Username, Reason: ReasonExpired})
			continue
		}
		if a.TrafficQuota <= 0 {
			continue
		}
		used, err := s.usedTraffic(a.Id)
		if err != nil {
			return nil, err
		}
		if used >= a.TrafficQuota {
			out = append(out, LimitBreach{AdminId: a.Id, Username: a.Username, Reason: ReasonQuotaExceeded})
		}
	}
	return out, nil
}

// usedTraffic sums the traffic of the clients this admin owns. Summing their
// inbounds instead counted every reseller on a shared inbound against each
// other's quota.
func (s *AdminService) usedTraffic(adminId int) (int64, error) {
	usage, err := UsageFor(adminId)
	return usage.Total(), err
}

// Deactivate switches a non-super admin off, ends their sessions and takes
// down the inbounds they created. Inbounds merely assigned to them belong to
// someone else and stay up. Returns whether xray needs a restart.
func (s *AdminService) Deactivate(inboundSvc *service.InboundService, id int, reason string) (bool, error) {
	db := database.GetDB()
	res := db.Model(&model.User{}).
		Where("id = ? AND role <> ?", id, string(model.RoleSuperAdmin)).
		Updates(map[string]any{
			"enabled":         false,
			"disabled_reason": reason,
			"login_epoch":     gorm.Expr("login_epoch + 1"),
		})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, ErrAdminNotFound
	}
	var ids []int
	if err := db.Model(&model.Inbound{}).Where("user_id = ? AND enable = ?", id, true).Pluck("id", &ids).Error; err != nil {
		return false, err
	}
	needRestart := false
	var firstErr error
	for _, ibId := range ids {
		nr, err := inboundSvc.SetInboundEnable(ibId, false)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		needRestart = needRestart || nr
	}
	return needRestart, firstErr
}
