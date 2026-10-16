package panel

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func seedInboundWithTraffic(t *testing.T, ownerId int, tag string, up, down int64) int {
	t.Helper()
	ib := model.Inbound{UserId: ownerId, Tag: tag, Remark: tag, Protocol: model.VLESS, Enable: true,
		Up: up, Down: down, Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}
	if err := database.GetDB().Create(&ib).Error; err != nil {
		t.Fatalf("inbound %s: %v", tag, err)
	}
	return ib.Id
}

// Only the limits that were set count, only enabled non-super admins are
// looked at, and assigned traffic counts the same as created traffic.
func TestBreachesFindExpiryAndQuota(t *testing.T) {
	s := newAdminService(t)
	const now = int64(1_800_000_000_000)

	over, _ := s.Create(AdminSpec{Username: "over", Password: "pw-over-123", Role: model.RoleReseller, Enabled: true, TrafficQuota: 1000})
	seedInboundWithTraffic(t, over.Id, "over-a", 600, 300)
	// 100 more arrives on an inbound handed to them: 1000 used, quota met.
	handed := seedInboundWithTraffic(t, firstAdminId(t), "handed", 50, 50)
	if err := s.SetAssignments(over.Id, []int{handed}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	under, _ := s.Create(AdminSpec{Username: "under", Password: "pw-under-123", Role: model.RoleReseller, Enabled: true, TrafficQuota: 5000})
	seedInboundWithTraffic(t, under.Id, "under-a", 600, 300)

	expired, _ := s.Create(AdminSpec{Username: "expired", Password: "pw-exp-123", Role: model.RoleReseller, Enabled: true, ExpireAt: now - 1})
	s.Create(AdminSpec{Username: "future", Password: "pw-fut-123", Role: model.RoleReseller, Enabled: true, ExpireAt: now + 1})
	s.Create(AdminSpec{Username: "nolimits", Password: "pw-nol-123", Role: model.RoleReseller, Enabled: true})

	// A super_admin with a stale expiry is never a breach.
	if err := database.GetDB().Model(&model.User{}).Where("id = ?", firstAdminId(t)).Update("expire_at", now-1).Error; err != nil {
		t.Fatalf("stale super expiry: %v", err)
	}

	breaches, err := s.Breaches(now)
	if err != nil {
		t.Fatalf("Breaches: %v", err)
	}
	got := map[int]string{}
	for _, b := range breaches {
		got[b.AdminId] = b.Reason
	}
	want := map[int]string{over.Id: ReasonQuotaExceeded, expired.Id: ReasonExpired}
	if len(got) != len(want) {
		t.Fatalf("breaches = %v, want %v", got, want)
	}
	for id, reason := range want {
		if got[id] != reason {
			t.Fatalf("admin %d reason = %q, want %q (all: %v)", id, got[id], reason, got)
		}
	}
}

// Deactivation ends sessions, records why, and takes down the inbounds the
// admin created -- but not the one a super_admin merely assigned to them.
func TestDeactivateLowersCreatedInboundsOnly(t *testing.T) {
	s := newAdminService(t)
	agent, _ := s.Create(AdminSpec{Username: "agent", Password: "pw-agent-123", Role: model.RoleReseller, Enabled: true})
	created := seedInboundWithTraffic(t, agent.Id, "agent-made", 0, 0)
	assigned := seedInboundWithTraffic(t, firstAdminId(t), "boss-made", 0, 0)
	if err := s.SetAssignments(agent.Id, []int{assigned}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	before := reload(t, agent.Id)

	if _, err := s.Deactivate(&service.InboundService{}, agent.Id, ReasonQuotaExceeded); err != nil {
		t.Logf("deactivate returned %v (inbound push without xray); checking stored state", err)
	}

	after := reload(t, agent.Id)
	if after.Enabled {
		t.Fatal("admin still enabled")
	}
	if after.DisabledReason != ReasonQuotaExceeded {
		t.Fatalf("disabled_reason = %q, want %q", after.DisabledReason, ReasonQuotaExceeded)
	}
	if after.LoginEpoch <= before.LoginEpoch {
		t.Fatal("sessions were not ended")
	}
	var createdRow, assignedRow model.Inbound
	database.GetDB().First(&createdRow, created)
	database.GetDB().First(&assignedRow, assigned)
	if createdRow.Enable {
		t.Fatal("the inbound the admin created is still enabled")
	}
	if !assignedRow.Enable {
		t.Fatal("the super_admin's assigned inbound was taken down too")
	}
}

// The last line of defence: a super_admin can never be switched off this way.
func TestDeactivateRefusesSuperAdmin(t *testing.T) {
	s := newAdminService(t)
	me := firstAdminId(t)
	if _, err := s.Deactivate(&service.InboundService{}, me, ReasonExpired); err == nil {
		t.Fatal("deactivating a super_admin was accepted")
	}
	if !reload(t, me).Enabled {
		t.Fatal("super_admin was disabled")
	}
}

// Re-enabling by hand clears the recorded reason.
func TestUpdateEnablingClearsDisabledReason(t *testing.T) {
	s := newAdminService(t)
	agent, _ := s.Create(AdminSpec{Username: "agent", Password: "pw-agent-123", Role: model.RoleReseller, Enabled: true})
	if _, err := s.Deactivate(&service.InboundService{}, agent.Id, ReasonExpired); err != nil {
		t.Logf("deactivate: %v", err)
	}
	if err := s.Update(agent.Id, AdminSpec{Role: model.RoleReseller, Enabled: true}); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	after := reload(t, agent.Id)
	if !after.Enabled || after.DisabledReason != "" {
		t.Fatalf("after re-enable: enabled=%v reason=%q", after.Enabled, after.DisabledReason)
	}
}
