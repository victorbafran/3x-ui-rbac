package service

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// A device over its limit is cut off at every entry server that is switched
// on, for as long as the panel bans it; an exit carries no customer and a
// switched-off front is not where anybody connects.
func TestABanReachesEveryEnabledEntryServerOnly(t *testing.T) {
	setupRelayDB(t)
	key := filepath.Join(t.TempDir(), "front.key")
	if err := os.WriteFile(key, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedRelays(t,
		model.Relay{Name: "iran-a", Address: "203.0.113.1", SSHPort: 22, KeyPath: key, Tunnel: "backpack", Role: model.RelayFront, Enabled: true},
		model.Relay{Name: "iran-b", Address: "203.0.113.2", SSHPort: 2222, KeyPath: key, Tunnel: "backpack", Role: model.RelayFront, Enabled: true},
		model.Relay{Name: "iran-off", Address: "203.0.113.3", KeyPath: key, Tunnel: "backpack", Role: model.RelayFront, Enabled: true},
		model.Relay{Name: "uk", Address: "198.51.100.9", KeyPath: key, Tunnel: "wireguard", Role: model.RelayExit, Enabled: true},
	)
	// The column defaults to true, so a false in the row never reaches it.
	if err := database.GetDB().Model(&model.Relay{}).Where("name = ?", "iran-off").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}

	var reached []string
	saved := banOnFront
	banOnFront = func(_ context.Context, c relay.Credentials, ips []string, d time.Duration) error {
		if !slices.Equal(ips, []string{"5.22.109.78"}) || d != 30*time.Minute {
			t.Errorf("%s was handed %v for %s", c.Address, ips, d)
		}
		reached = append(reached, c.Address)
		return nil
	}
	t.Cleanup(func() { banOnFront = saved })

	(&RelayService{}).BanOnFronts([]string{"5.22.109.78"}, 30*time.Minute)
	slices.Sort(reached)
	if want := []string{"203.0.113.1", "203.0.113.2"}; !slices.Equal(reached, want) {
		t.Errorf("the ban reached %v, want the enabled entry servers %v", reached, want)
	}
}
