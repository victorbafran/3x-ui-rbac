package service

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Two admins, one inbound and one client each, plus a client that spans both.
type guardWorld struct {
	a, b           int
	inA, inB       int
	emailA, emailB string
	shared         string
}

func seedGuardWorld(t *testing.T) guardWorld {
	t.Helper()
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	w := guardWorld{emailA: "a@example.com", emailB: "b@example.com", shared: "shared@example.com"}
	w.a = seedOwnedClient(t, "guard-a", "gtag-a", w.emailA)
	w.b = seedOwnedClient(t, "guard-b", "gtag-b", w.emailB)
	db := database.GetDB()
	var inA, inB model.Inbound
	if err := db.Where("tag = ?", "gtag-a").First(&inA).Error; err != nil {
		t.Fatalf("inbound a: %v", err)
	}
	if err := db.Where("tag = ?", "gtag-b").First(&inB).Error; err != nil {
		t.Fatalf("inbound b: %v", err)
	}
	w.inA, w.inB = inA.Id, inB.Id
	// Owned by B, attached to both inbounds: reachable by B alone.
	shared := model.ClientRecord{Email: w.shared, Enable: true, AdminId: w.b}
	if err := db.Create(&shared).Error; err != nil {
		t.Fatalf("shared client: %v", err)
	}
	for _, id := range []int{inA.Id, inB.Id} {
		if err := db.Create(&model.ClientInbound{ClientId: shared.Id, InboundId: id}).Error; err != nil {
			t.Fatalf("attach shared: %v", err)
		}
	}
	return w
}

// A body naming someone else's inbound, or an inbound that does not exist,
// must be refused before anything is written.
func TestForeignInboundsNamesWhatTheAdminMayNotTouch(t *testing.T) {
	w := seedGuardWorld(t)
	svc := &ClientService{}
	foreign, err := svc.ForeignInbounds(w.a, []int{w.inA, w.inB, 9999})
	if err != nil {
		t.Fatalf("ForeignInbounds: %v", err)
	}
	if len(foreign) != 2 || foreign[0] != w.inB || foreign[1] != 9999 {
		t.Fatalf("foreign = %v, want [%d 9999]", foreign, w.inB)
	}
	none, err := svc.ForeignInbounds(0, []int{w.inA, w.inB, 9999})
	if err != nil || none != nil {
		t.Fatalf("super_admin (0) must own everything, got %v %v", none, err)
	}
}

// A client owned by another admin is not this admin's to touch, even when it
// sits on an inbound they share, and an unknown email is refused outright.
func TestForeignEmailsRefusesSharedAndUnknownClients(t *testing.T) {
	w := seedGuardWorld(t)
	svc := &ClientService{}
	foreign, err := svc.ForeignEmails(w.a, []string{w.emailA, w.emailB, w.shared, "ghost@example.com"})
	if err != nil {
		t.Fatalf("ForeignEmails: %v", err)
	}
	want := []string{w.emailB, w.shared, "ghost@example.com"}
	if len(foreign) != len(want) {
		t.Fatalf("foreign = %v, want %v", foreign, want)
	}
	for i := range want {
		if foreign[i] != want[i] {
			t.Fatalf("foreign = %v, want %v", foreign, want)
		}
	}
}

// Being handed an inbound does not hand over the customers on it.
func TestOwnedEmailSetIgnoresAssignment(t *testing.T) {
	w := seedGuardWorld(t)
	svc := &ClientService{}
	if err := database.GetDB().Create(&model.AdminInbound{AdminId: w.a, InboundId: w.inB}).Error; err != nil {
		t.Fatalf("assign: %v", err)
	}
	set, err := svc.OwnedEmailSet(w.a)
	if err != nil {
		t.Fatalf("OwnedEmailSet: %v", err)
	}
	if !set[w.emailA] {
		t.Fatalf("own client missing after assignment; set = %v", set)
	}
	for _, e := range []string{w.emailB, w.shared} {
		if set[e] {
			t.Fatalf("%s came with the assigned inbound; it belongs to the other admin", e)
		}
	}
}

// A subscription id is as good as an email for reaching a client's links.
func TestForeignSubIdAndClientId(t *testing.T) {
	w := seedGuardWorld(t)
	svc := &ClientService{}
	db := database.GetDB()
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", w.emailB).Update("sub_id", "sub-b").Error; err != nil {
		t.Fatalf("set sub id: %v", err)
	}
	if foreign, _ := svc.ForeignSubId(w.a, "sub-b"); !foreign {
		t.Fatal("another admin's subId was not refused")
	}
	if foreign, _ := svc.ForeignSubId(w.a, "no-such-sub"); !foreign {
		t.Fatal("an unknown subId was not refused")
	}
	var mine model.ClientRecord
	if err := db.Where("email = ?", w.emailA).First(&mine).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if foreign, _ := svc.ForeignClientId(w.a, mine.Id); foreign {
		t.Fatal("the admin's own client id was refused")
	}
	if foreign, _ := svc.ForeignClientId(w.a, 99999); !foreign {
		t.Fatal("an unknown client id was not refused")
	}
}

// A live push asks who owns the few clients it names. Loading every online
// reseller's whole list for each push grew with their size, not with the push.
func TestOwnedEmailsAmongAnswersOnlyForTheEmailsAsked(t *testing.T) {
	w := seedGuardWorld(t)
	svc := &ClientService{}
	got, err := svc.OwnedEmailsAmong([]int{w.a, w.b}, []string{w.emailA, w.shared, "ghost@example.com", w.emailA})
	if err != nil {
		t.Fatalf("OwnedEmailsAmong: %v", err)
	}
	if want := map[int]map[string]bool{w.a: {w.emailA: true}, w.b: {w.shared: true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("owned = %v, want %v", got, want)
	}
	got, err = svc.OwnedEmailsAmong([]int{w.a}, []string{w.emailB})
	if err != nil || len(got) != 0 {
		t.Fatalf("an admin not asked about answered: %v %v", got, err)
	}
}
