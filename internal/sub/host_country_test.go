package sub

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func linkNames(t *testing.T, links []string) []string {
	t.Helper()
	var names []string
	for _, line := range strings.Split(strings.Join(links, "\n"), "\n") {
		u, err := url.Parse(line)
		if err != nil {
			t.Fatalf("unparsable link %q: %v", line, err)
		}
		names = append(names, u.Fragment)
	}
	return names
}

func hereIs(t *testing.T, label string) {
	t.Helper()
	saved := localCountryLabel
	localCountryLabel = func() string { return label }
	t.Cleanup(func() { localCountryLabel = saved })
}

// On an inbound that offers countries every entry is named for where it leaves:
// an exit by its own label, the host that stays here by this server's.
func TestEachCountryEntryIsNamedForWhereItLeaves(t *testing.T) {
	seedSubDB(t)
	hereIs(t, "🇩🇪 Germany")
	if err := database.GetDB().Create(&model.Relay{Id: 9, Name: "uk", Label: "🇬🇧 United Kingdom", Address: "198.51.100.9", KeyPath: "/k", Tunnel: "wireguard", Role: model.RelayExit}).Error; err != nil {
		t.Fatal(err)
	}
	ib := seedSubInbound(t, "s1", "n", 4432, 1, `{"network":"tcp","security":"none"}`)
	seedHost(t, &model.Host{InboundId: ib.Id, SortOrder: 1, Remark: "ennn", Address: "tun.example.ir", Exit: "wgr9", VlessRoute: "9"})
	seedHost(t, &model.Host{InboundId: ib.Id, SortOrder: 2, Remark: "here", Address: "tun.example.ir"})

	links, _, _, _, err := NewSubService("").GetSubs("s1", "req.example.com")
	if err != nil {
		t.Fatal(err)
	}
	names := linkNames(t, links)
	if len(names) != 2 || !strings.HasPrefix(names[0], "🇬🇧 United Kingdom ") || !strings.HasPrefix(names[1], "🇩🇪 Germany ") {
		t.Errorf("names %q; want the UK entry and then the German one, each led by its country", names)
	}
}

// Hosts that offer no country, a CDN list say, keep exactly the names they had.
func TestHostsWithoutAnExitKeepTheirNames(t *testing.T) {
	seedSubDB(t)
	hereIs(t, "🇩🇪 Germany")
	ib := seedSubInbound(t, "s1", "n", 4432, 1, `{"network":"tcp","security":"none"}`)
	seedHost(t, &model.Host{InboundId: ib.Id, Remark: "cdn", Address: "cdn.example.com"})

	links, _, _, _, err := NewSubService("").GetSubs("s1", "req.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if names := linkNames(t, links); len(names) != 1 || strings.Contains(names[0], "Germany") {
		t.Errorf("names %q; a host offering no country was renamed", names)
	}
}

func TestACountryIsNamedOnce(t *testing.T) {
	for _, tc := range []struct{ remark, country, want string }{
		{"n-ennn-n@e", "🇬🇧 United Kingdom", "🇬🇧 United Kingdom n-ennn-n@e"},
		{"🇬🇧 انگلیس ali", "🇬🇧 United Kingdom", "🇬🇧 انگلیس ali"},
		{"", "🇬🇧 United Kingdom", "🇬🇧 United Kingdom"},
		{"n-ennn-n@e", "", "n-ennn-n@e"},
	} {
		if got := withCountry(tc.remark, tc.country); got != tc.want {
			t.Errorf("withCountry(%q, %q) = %q, want %q", tc.remark, tc.country, got, tc.want)
		}
	}
}
