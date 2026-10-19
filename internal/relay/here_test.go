package relay

import "testing"

// Three services, three shapes. The panel has to read all of them or it will
// report "nowhere" for a server that two of the three could place: measured,
// ipinfo refuses two of the owner's three machines outright.
func TestEachLookupServiceIsUnderstood(t *testing.T) {
	for name, tc := range map[string]struct {
		body map[string]any
		want Here
	}{
		"ipwho.is": {
			map[string]any{"country": "Germany", "country_code": "DE", "city": "Frankfurt"},
			Here{Country: "Germany", CountryCode: "DE", City: "Frankfurt"},
		},
		"ip-api": {
			map[string]any{"country": "France", "countryCode": "FR", "city": "Gravelines"},
			Here{Country: "France", CountryCode: "FR", City: "Gravelines"},
		},
		// ipinfo puts the code in the field the others use for the name.
		"ipinfo": {
			map[string]any{"country": "NL", "city": "Amsterdam"},
			Here{Country: "", CountryCode: "NL", City: "Amsterdam"},
		},
	} {
		got, ok := fromFields(tc.body)
		if !ok {
			t.Errorf("%s: the reply was not understood at all", name)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %+v, wanted %+v", name, got, tc.want)
		}
	}
}

// A reply with no country is not an answer. Treating it as one would label a
// country wrongly, which is worse than asking the operator to type it.
func TestAReplyWithNoCountryIsNotAnAnswer(t *testing.T) {
	for _, body := range []map[string]any{
		{},
		{"city": "Somewhere"},
		{"country": ""},
	} {
		if got, ok := fromFields(body); ok {
			t.Errorf("%v was accepted as a location: %+v", body, got)
		}
	}
}

// Known is what decides whether the panel offers itself as an exit at all.
func TestOnlyALocationWithACodeCountsAsKnown(t *testing.T) {
	if (Here{Country: "Germany"}).Known() {
		t.Error("a country with no code was treated as known, and no flag can be built from it")
	}
	if !(Here{CountryCode: "DE"}).Known() {
		t.Error("a code alone should be enough to place the server")
	}
}
