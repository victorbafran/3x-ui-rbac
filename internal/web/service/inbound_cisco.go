package service

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/ocserv"
)

// DesiredCiscoInstances derives the ocserv sidecar configs this panel should
// be running: one instance per enabled local cisco inbound, serving only the
// clients that are both enabled in the inbound settings and not
// depletion-disabled in client_traffics. Mirrors DesiredMtprotoInstances, so
// the two sidecars agree on what "an active client" means. Inbounds whose
// every client is filtered away are omitted, which makes Reconcile stop them.
func (s *InboundService) DesiredCiscoInstances() ([]ocserv.Instance, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).
		Where("protocol = ? AND enable = ? AND node_id IS NULL", model.Cisco, true).
		Find(&inbounds).Error
	if err != nil {
		return nil, err
	}
	if len(inbounds) == 0 {
		return nil, nil
	}

	instances := make([]ocserv.Instance, 0, len(inbounds))
	for _, ib := range inbounds {
		if inst, ok := ocserv.InstanceFromInbound(ib); ok {
			instances = append(instances, inst)
		}
	}
	emails := make([]string, 0)
	for _, inst := range instances {
		emails = append(emails, inst.UserNames()...)
	}
	disabled, err := trafficDisabledEmails(db, emails)
	if err != nil {
		return nil, err
	}
	served := instances[:0]
	for _, inst := range instances {
		kept := make([]ocserv.User, 0, len(inst.Users))
		for _, u := range inst.Users {
			if _, off := disabled[u.Name]; !off {
				kept = append(kept, u)
			}
		}
		inst.Users = kept
		if len(kept) > 0 {
			served = append(served, inst)
		}
	}
	return served, nil
}
