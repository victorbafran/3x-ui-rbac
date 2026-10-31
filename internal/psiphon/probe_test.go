package psiphon

import (
	"slices"
	"testing"
	"time"
)

func shortSettle(t *testing.T) {
	t.Helper()
	saved := probeSettle
	probeSettle = time.Second
	t.Cleanup(func() { probeSettle = saved })
}

// A first start in an empty data directory lists no country; only a second
// start of the same directory reads the list the first one fetched.
func TestDiscoverRegionsStartsTheProbeTwiceToReadTheList(t *testing.T) {
	useFakeClient(t, "probe")
	shortSettle(t)
	got, err := DiscoverRegions(t.Context())
	if err != nil {
		t.Fatalf("DiscoverRegions: %v", err)
	}
	if !slices.Equal(got, []string{"DE", "NL", "US"}) {
		t.Fatalf("regions = %v, want the list from the second start", got)
	}
}

// The panel offered 14 of 26 countries: the probe kept the first start's partial
// list and never started again; a short notice around the whole list must not cut it.
func TestDiscoverRegionsKeepsEveryCountryAnyNoticeListed(t *testing.T) {
	useFakeClient(t, "partial")
	shortSettle(t)
	got, err := DiscoverRegions(t.Context())
	if err != nil {
		t.Fatalf("DiscoverRegions: %v", err)
	}
	if want := []string{"AT", "DE", "JP", "NL", "PL", "US"}; !slices.Equal(got, want) {
		t.Fatalf("regions = %v, want %v", got, want)
	}
}

// A probe that never lists a country still leaves the operator countries to
// pick from, in a copy the caller may change.
func TestDiscoverRegionsFallsBackWhenNothingIsListed(t *testing.T) {
	useFakeClient(t, "silent")
	saved := probeBudget
	probeBudget = 2 * time.Second
	t.Cleanup(func() { probeBudget = saved })

	got, err := DiscoverRegions(t.Context())
	if err != nil || !slices.Equal(got, FallbackRegions) {
		t.Fatalf("DiscoverRegions = %v, %v; want the known countries", got, err)
	}
	got[0] = "XX"
	if FallbackRegions[0] != "AT" {
		t.Fatal("the caller's copy is the package's own list")
	}
}
