package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// seedClientOwnedBy creates a client for an admin, optionally in a group.
func seedClientOwnedBy(t *testing.T, adminId int, email, group string) {
	t.Helper()
	cr := model.ClientRecord{Email: email, Enable: true, AdminId: adminId, Group: group}
	if err := database.GetDB().Create(&cr).Error; err != nil {
		t.Fatalf("client %s: %v", email, err)
	}
}

// The online list arrives from xray naming every client on the panel. Matching
// it against all clients showed a reseller other admins' customers as online,
// even when the reseller had no customers at all.
func TestOnlineEmailsAreScopedToTheCaller(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	bossId := seedOwnedClient(t, "boss-sum", "sum-tag", "boss-client@example.com")
	resellerId := seedOwnedClient(t, "res-sum", "res-tag", "res-client@example.com")
	_ = bossId

	// xray reports both as online.
	fromXray := []string{"boss-client@example.com", "res-client@example.com"}

	q := newClientQuery(database.GetDB(), time.Now().UnixMilli(), 0, 0)
	q.ownerId = resellerId
	emails, count, err := q.onlineEmails(fromXray)
	if err != nil {
		t.Fatalf("onlineEmails: %v", err)
	}
	if count != 1 || len(emails) != 1 || emails[0] != "res-client@example.com" {
		t.Fatalf("reseller sees online %v (count %d), want only their own", emails, count)
	}

	// The unscoped path still sees both.
	q.ownerId = 0
	emails, count, err = q.onlineEmails(fromXray)
	if err != nil {
		t.Fatalf("onlineEmails unscoped: %v", err)
	}
	if count != 2 || len(emails) != 2 {
		t.Fatalf("unscoped sees %v (count %d), want both", emails, count)
	}
}

// Group names are a panel-wide naming layer: a reseller must not learn the
// names another admin uses for their customers.
func TestGroupNamesAreScopedToTheCaller(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	mineId := seedOwnedClient(t, "owner-g", "g-tag", "g-mine@example.com")
	theirsId := seedOwnedClient(t, "other-g", "g-tag2", "g-theirs@example.com")
	seedClientOwnedBy(t, mineId, "grouped-mine@example.com", "mine-group")
	seedClientOwnedBy(t, theirsId, "grouped-theirs@example.com", "secret-group")

	svc := &ClientService{}
	mine, err := svc.listGroupNames(mineId)
	if err != nil {
		t.Fatalf("listGroupNames: %v", err)
	}
	for _, g := range mine {
		if g == "secret-group" {
			t.Fatalf("another admin's group name leaked: %v", mine)
		}
	}
	found := false
	for _, g := range mine {
		if g == "mine-group" {
			found = true
		}
	}
	if !found {
		t.Fatalf("own group missing from %v", mine)
	}

	all, err := svc.listGroupNames(0)
	if err != nil {
		t.Fatalf("listGroupNames(0): %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("unscoped caller should see both groups, got %v", all)
	}
}
