package psiphon

import (
	"slices"
	"testing"
	"time"
)

// A first start in an empty data directory lists no country; only a second
// start of the same directory reads the list the first one fetched.
func TestDiscoverRegionsStartsTheProbeTwiceToReadTheList(t *testing.T) {
	useFakeClient(t, "probe")
	got, err := DiscoverRegions(t.Context())
	if err != nil {
		t.Fatalf("DiscoverRegions: %v", err)
	}
	if !slices.Equal(got, []string{"DE", "NL", "US"}) {
		t.Fatalf("regions = %v, want the list from the second start", got)
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
		t.Fatalf("DiscoverRegions = %v, %v; want the 26 known countries", got, err)
	}
	got[0] = "XX"
	if FallbackRegions[0] != "AT" {
		t.Fatal("the caller's copy is the package's own list")
	}
}
