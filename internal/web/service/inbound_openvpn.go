package service

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/openvpn"
)

// DesiredOpenVPNInstances derives the sidecar configs this panel should be
// running: one per enabled local OpenVPN inbound, serving only the clients
// that are both enabled in the inbound settings and not depletion-disabled in
// client_traffics. Mirrors DesiredCiscoInstances so the two sidecars agree on
// what "an active client" means. An inbound whose every client is filtered
// away is omitted, which is what makes Reconcile stop it.
func (s *InboundService) DesiredOpenVPNInstances() ([]openvpn.Instance, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).
		Where("protocol = ? AND enable = ? AND node_id IS NULL", model.OpenVPN, true).
		Find(&inbounds).Error
	if err != nil {
		return nil, err
	}
	if len(inbounds) == 0 {
		return nil, nil
	}

	instances := make([]openvpn.Instance, 0, len(inbounds))
	for _, ib := range inbounds {
		if inst, ok := openvpn.InstanceFromInbound(ib); ok {
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
		kept := make([]openvpn.User, 0, len(inst.Users))
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
