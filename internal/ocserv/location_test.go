package ocserv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The dropdown is the whole feature: before asking for a password, ocserv
// advertises "Group: [Germany|Netherlands]" and the customer picks where to
// come out. Measured against a real AnyConnect client on ocserv 1.2.4.
func TestRenderConfigOffersEveryCountryInTheDropdown(t *testing.T) {
	cfg := configFor(t, Instance{
		Id: 1, Port: 443,
		Locations: []Location{
			{Name: "germany", Label: "Germany"},
			{Name: "netherlands", Label: "Netherlands", Exit: "wg0"},
		},
	})
	for _, want := range []string{
		"select-group = germany[Germany]",
		"select-group = netherlands[Netherlands]",
		"config-per-group = ",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("missing %q from the rendered config:\n%s", want, cfg)
		}
	}
}

// An inbound with no countries must render exactly as it always did. Once any
// select-group exists, ocserv refuses every client that does not name a group,
// so writing the directive onto an inbound that offers nothing would take the
// whole inbound offline.
func TestRenderConfigOffersNoDropdownWithoutCountries(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 443})
	for _, unwanted := range []string{"select-group", "config-per-group"} {
		if strings.Contains(cfg, unwanted) {
			t.Fatalf("%q was written for an inbound with no locations; every client would be refused:\n%s", unwanted, cfg)
		}
	}
}

// A label is what the customer reads, and ocserv puts it inside brackets. A
// label carrying one of those brackets would cut the entry short, so the
// customer would see a country whose name stops halfway.
func TestLabelCannotBreakTheDropdownEntry(t *testing.T) {
	cfg := configFor(t, Instance{
		Id: 1, Port: 443,
		Locations: []Location{{Name: "nl", Label: "Nether[lands]"}},
	})
	if !strings.Contains(cfg, "select-group = nl[Netherlands]") {
		t.Fatalf("the brackets were not taken out of the label:\n%s", cfg)
	}
}

// With no label the customer should still read something, not an empty entry.
func TestACountryWithNoLabelShowsItsName(t *testing.T) {
	cfg := configFor(t, Instance{Id: 1, Port: 443, Locations: []Location{{Name: "turkey"}}})
	if !strings.Contains(cfg, "select-group = turkey[turkey]") {
		t.Fatalf("a country with no label did not fall back to its name:\n%s", cfg)
	}
}

// Two countries handing out the same addresses would route each other's
// customers, and a country sharing an inbound's own pool would do the same.
// The pools are derived rather than asked for precisely so that cannot happen.
func TestEveryDerivedPoolIsItsOwn(t *testing.T) {
	seen := make(map[string]string)
	for id := 0; id < 64; id++ {
		pool := DefaultPool(id)
		if where, clash := seen[pool]; clash {
			t.Fatalf("inbound %d takes the pool already used by %s", id, where)
		}
		seen[pool] = "inbound " + string(rune('0'+id%10))
		for index := 0; index < locationsPerInbound; index++ {
			lp := LocationPool(id, index)
			if where, clash := seen[lp]; clash {
				t.Fatalf("location %d of inbound %d takes the pool already used by %s", index, id, where)
			}
			seen[lp] = "a location"
		}
	}
}

// Every location also needs a routing table nothing else is using, or removing
// one country would flush another country's route.
func TestEveryCountryGetsItsOwnRoutingTable(t *testing.T) {
	seen := make(map[int]struct{})
	for id := 0; id < 64; id++ {
		for index := 0; index < locationsPerInbound; index++ {
			table := routeTableFor(id, index)
			if _, clash := seen[table]; clash {
				t.Fatalf("location %d of inbound %d reuses routing table %d", index, id, table)
			}
			if table < 256 {
				t.Fatalf("routing table %d is inside the range Linux and hand-written rules use", table)
			}
			seen[table] = struct{}{}
		}
	}
}

// The per-country file is what ocserv reads when a client picks that country:
// the pool it hands out, and the resolvers that go with it.
func TestTheCountryFileCarriesItsOwnPool(t *testing.T) {
	inst := Instance{Id: 2, Port: 443, DNS: []string{"1.1.1.1"}}
	body := renderGroupConfig(inst, Location{Name: "nl", DNS: []string{"9.9.9.9"}}, 0)
	if !strings.Contains(body, "ipv4-network = "+LocationPool(2, 0)) {
		t.Fatalf("the country was not given its derived pool:\n%s", body)
	}
	if !strings.Contains(body, "dns = 9.9.9.9") {
		t.Fatalf("the country's own resolver was dropped:\n%s", body)
	}
}

// A country that names no resolver should inherit the inbound's, not end up
// with none at all — a tunnel with no DNS resolves nothing.
func TestACountryWithNoResolverInheritsTheInbound(t *testing.T) {
	inst := Instance{Id: 2, Port: 443, DNS: []string{"8.8.4.4"}}
	body := renderGroupConfig(inst, Location{Name: "nl"}, 1)
	if !strings.Contains(body, "dns = 8.8.4.4") {
		t.Fatalf("the inbound's resolver was not inherited:\n%s", body)
	}
}

// The password file is colon-separated and its group field comma-separated,
// and each country is also a filename. A name carrying any of those would let
// one country rewrite another's entry.
func TestACountryNameCannotCarryASeparator(t *testing.T) {
	for _, bad := range []string{"", "de:nl", "de,nl", "../etc", "de/nl", "de nl", ".", ".."} {
		if err := ValidLocationName(bad); err == nil {
			t.Errorf("%q was accepted as a country name", bad)
		}
	}
	if err := ValidLocationName("netherlands"); err != nil {
		t.Errorf("an ordinary name was refused: %v", err)
	}
}

// A name that cannot be used takes itself out of the list; it must not take
// the inbound's other countries with it.
func TestABrokenCountryDoesNotTakeTheOthersWithIt(t *testing.T) {
	inst := Instance{Id: 1, Port: 443, Locations: []Location{
		{Name: "germany"},
		{Name: "bad:name"},
		{Name: "germany"},
		{Name: "netherlands"},
	}}
	locs, problems := inst.locations()
	var names []string
	for _, l := range locs {
		names = append(names, l.Name)
	}
	if strings.Join(names, ",") != "germany,netherlands" {
		t.Fatalf("expected the two usable countries, got %v", names)
	}
	if len(problems) != 2 {
		t.Fatalf("expected the bad name and the duplicate to be reported, got %v", problems)
	}
}

// ocserv reads the group list once, at startup. A country added, renamed,
// renumbered or pointed at a different exit has to restart the instance, or
// the customer picks a country the running server has never heard of.
func TestChangingTheCountriesForcesARestart(t *testing.T) {
	base := Instance{Id: 1, Port: 443, Locations: []Location{{Name: "germany", Label: "Germany"}}}
	for _, tc := range []struct {
		name string
		inst Instance
	}{
		{"a country added", Instance{Id: 1, Port: 443, Locations: []Location{
			{Name: "germany", Label: "Germany"}, {Name: "netherlands"},
		}}},
		{"a label changed", Instance{Id: 1, Port: 443, Locations: []Location{{Name: "germany", Label: "Berlin"}}}},
		{"an exit changed", Instance{Id: 1, Port: 443, Locations: []Location{{Name: "germany", Label: "Germany", Exit: "wg0"}}}},
		{"the countries removed", Instance{Id: 1, Port: 443}},
	} {
		if base.structuralFingerprint() == tc.inst.structuralFingerprint() {
			t.Errorf("%s did not force a restart; ocserv would keep offering the old list", tc.name)
		}
	}
}

// Reordering two countries swaps the pools they hand out, because a pool is
// derived from position. That is a restart too, even though the names match.
func TestReorderingTheCountriesForcesARestart(t *testing.T) {
	a := Instance{Id: 1, Port: 443, Locations: []Location{{Name: "germany"}, {Name: "netherlands"}}}
	b := Instance{Id: 1, Port: 443, Locations: []Location{{Name: "netherlands"}, {Name: "germany"}}}
	if a.structuralFingerprint() == b.structuralFingerprint() {
		t.Fatal("swapping two countries did not force a restart, so each would keep the other's addresses")
	}
}

// What a client may pick is written into the password file's group field.
func TestThePasswordFileListsTheCountriesAClientMayPick(t *testing.T) {
	out, problems := renderPasswd([]User{
		{Name: "a@b", Login: "a@b", Password: "secret", Locations: []string{"germany", "netherlands"}},
	})
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if !strings.Contains(out, "a@b:germany,netherlands:") {
		t.Fatalf("the countries were not written into the group field:\n%s", out)
	}
}

// "*" is not "any group". Measured on ocserv 1.2.4: a user written with "*"
// who asks for a named group is refused as not a member. It is only correct
// where there are no groups at all.
func TestAClientWithNoCountriesIsWrittenWithAStar(t *testing.T) {
	out, _ := renderPasswd([]User{{Name: "a@b", Login: "a@b", Password: "secret"}})
	if !strings.Contains(out, "a@b:*:") {
		t.Fatalf("expected the star for a client with no countries:\n%s", out)
	}
}

// Turning countries on must not cut off the customers who were connecting
// before they existed: on ocserv 1.2.4 a client entitled to no group cannot
// connect at all once any select-group is configured.
func TestAClientNamingNoCountryGetsThemAll(t *testing.T) {
	got := entitlement(nil, []string{"germany", "netherlands"})
	if strings.Join(got, ",") != "germany,netherlands" {
		t.Fatalf("expected every country, got %v", got)
	}
}

// An operator who narrows a client to one country gets exactly that.
func TestAClientNamingOneCountryKeepsOnlyIt(t *testing.T) {
	got := entitlement([]string{"germany"}, []string{"germany", "netherlands"})
	if strings.Join(got, ",") != "germany" {
		t.Fatalf("expected the single country, got %v", got)
	}
}

// A country the operator has deleted must not be left in a client's entry,
// naming a group the running server no longer has.
func TestACountryNoLongerOfferedLeavesTheClientEntry(t *testing.T) {
	got := entitlement([]string{"germany", "turkey"}, []string{"germany", "netherlands"})
	if strings.Join(got, ",") != "germany" {
		t.Fatalf("expected the deleted country to be dropped, got %v", got)
	}
}

// writeGroupConfigs owns the directory. A file for a country that is no
// longer offered would keep handing out a pool nothing routes any more: the
// customer connects, picks it, and reaches nothing.
func TestACountryNoLongerOfferedLosesItsFile(t *testing.T) {
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })

	inst := Instance{Id: 5, Port: 443, Locations: []Location{{Name: "germany"}, {Name: "turkey"}}}
	if err := writeGroupConfigs(inst); err != nil {
		t.Fatalf("writing the countries: %v", err)
	}
	dir := perGroupDirForID(5)
	if _, err := os.Stat(filepath.Join(dir, "turkey")); err != nil {
		t.Fatalf("turkey was never written: %v", err)
	}

	inst.Locations = []Location{{Name: "germany"}}
	if err := writeGroupConfigs(inst); err != nil {
		t.Fatalf("rewriting the countries: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "turkey")); !os.IsNotExist(err) {
		t.Fatal("turkey kept its file after the operator stopped offering it")
	}
	if _, err := os.Stat(filepath.Join(dir, "germany")); err != nil {
		t.Fatalf("germany lost its file too: %v", err)
	}
}

// An inbound that stops offering countries altogether should be left with no
// directory at all, not an empty one ocserv would still be pointed at.
func TestRemovingEveryCountryRemovesTheDirectory(t *testing.T) {
	runtimeDirOverride = t.TempDir()
	t.Cleanup(func() { runtimeDirOverride = "" })

	inst := Instance{Id: 6, Port: 443, Locations: []Location{{Name: "germany"}}}
	if err := writeGroupConfigs(inst); err != nil {
		t.Fatalf("writing the countries: %v", err)
	}
	inst.Locations = nil
	if err := writeGroupConfigs(inst); err != nil {
		t.Fatalf("clearing the countries: %v", err)
	}
	if _, err := os.Stat(perGroupDirForID(6)); !os.IsNotExist(err) {
		t.Fatal("the directory outlived the last country")
	}
}

// A country with an exit must not be masqueraded on this host. The rule nat.go
// writes names no outgoing interface, so it would rewrite the packets heading
// into the tunnel as well, and the server at the far end would have no address
// left to route the answers back to.
func TestACountryWithAnExitIsNotMasqueradedHere(t *testing.T) {
	inst := Instance{Id: 3, Port: 443, Locations: []Location{
		{Name: "here"},
		{Name: "there", Exit: "wg0"},
	}}
	routes := inst.locationRoutes()
	if len(routes) != 2 {
		t.Fatalf("expected a route for each country, got %d", len(routes))
	}
	if routes[0].Exit != "" || routes[1].Exit != "wg0" {
		t.Fatalf("the exits did not survive: %+v", routes)
	}
	if routes[0].CIDR == routes[1].CIDR {
		t.Fatalf("both countries were given the same pool: %+v", routes)
	}
	if routes[0].Table == routes[1].Table {
		t.Fatalf("both countries were given the same routing table: %+v", routes)
	}
}
