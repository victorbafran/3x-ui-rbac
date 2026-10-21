package service

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func setupRelayDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	dbtest.InitDB(t, filepath.Join(dir, "x-ui.db"))
}

func seedRelays(t *testing.T, rows ...model.Relay) {
	t.Helper()
	for i := range rows {
		if err := database.GetDB().Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", rows[i].Name, err)
		}
	}
}

// An entry server is not a country to leave through: it has no WireGuard
// interface, so a cisco country pointed at one would send its customers into
// an interface that does not exist. The exit dropdown must not offer it.
func TestTheExitListNeverOffersAnEntryServer(t *testing.T) {
	setupRelayDB(t)
	seedRelays(t,
		model.Relay{Name: "netherlands", Address: "198.51.100.7", KeyPath: "/k1", Tunnel: "wireguard", Role: model.RelayExit, Enabled: true, Status: RelayReady},
		model.Relay{Name: "iran-front", Address: "203.0.113.9", KeyPath: "/k2", Tunnel: "backhaul", Role: model.RelayFront, Enabled: true, Status: RelayReady},
	)
	opts, err := (&RelayService{}).exitOptions()
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range opts {
		if o.Name == "iran-front" {
			t.Fatalf("the exit list offers the entry server %+v", o)
		}
	}
	found := false
	for _, o := range opts {
		found = found || o.Name == "netherlands"
	}
	if !found {
		t.Fatalf("the exit list lost the real exit: %+v", opts)
	}
}

// The inbound form offers entry servers for the address customers are
// given, and only those that are switched on.
func TestTheFrontListOffersEnabledEntryServersWithTheirName(t *testing.T) {
	setupRelayDB(t)
	seedRelays(t,
		model.Relay{Name: "netherlands", Address: "198.51.100.7", KeyPath: "/k1", Tunnel: "wireguard", Role: model.RelayExit, Enabled: true},
		model.Relay{Name: "iran-front", Address: "203.0.113.9", KeyPath: "/k2", Tunnel: "backpack", Role: model.RelayFront, Enabled: true, Status: RelayReady, PublicName: "tun.example.ir"},
		model.Relay{Name: "old-front", Address: "203.0.113.10", KeyPath: "/k3", Tunnel: "backhaul", Role: model.RelayFront, Enabled: false},
	)
	// gorm skips a false bool on create; switch the disabled one off for real.
	if err := database.GetDB().Model(&model.Relay{}).Where("name = ?", "old-front").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}

	fronts, err := (&RelayService{}).FrontOptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(fronts) != 1 || fronts[0].Name != "iran-front" {
		t.Fatalf("want only the enabled front, got %+v", fronts)
	}
	if fronts[0].PublicName != "tun.example.ir" || !fronts[0].Ready {
		t.Fatalf("the front lost its name or its state: %+v", fronts[0])
	}
}

// The public name goes straight into an inbound's share address, so it is
// held to the same rules: a host, no scheme, no port.
func TestAFrontsPublicNameFollowsTheShareAddressRules(t *testing.T) {
	s := &RelayService{}
	r, err := s.rowFrom(RelayRequest{
		Name: "iran", Address: "203.0.113.9", Role: "front", PublicName: " Tun.Example.IR. ",
	}, nil)
	if err != nil {
		t.Fatalf("a good name was refused: %v", err)
	}
	if r.PublicName != "tun.example.ir" {
		t.Fatalf("PublicName = %q, want tun.example.ir", r.PublicName)
	}
	for _, bad := range []string{"https://tun.example.ir", "tun.example.ir:443", "tun.example.ir/path"} {
		if _, err := s.rowFrom(RelayRequest{Name: "iran", Address: "203.0.113.9", Role: "front", PublicName: bad}, nil); err == nil {
			t.Errorf("the public name %q was accepted", bad)
		}
	}
	if r, err := s.rowFrom(RelayRequest{Name: "iran", Address: "203.0.113.9", Role: "front"}, nil); err != nil || r.PublicName != "" {
		t.Fatalf("an empty public name was not left empty: %q, %v", r.PublicName, err)
	}
}
