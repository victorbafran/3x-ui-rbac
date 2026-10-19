package relay

import "testing"

// The reply is one key per line. A mistyped separator once left the whole
// thing in a single field — the country code came back as the country, the
// city and two newlines — and the only symptom anybody saw was a relay with
// no flag. Nothing about that failure pointed at a separator.
func TestTheLocationReplyIsReadLineByLine(t *testing.T) {
	country, code, city := parseLocation("countryCode=NL\ncountry=Netherlands\ncity=Amsterdam\n")
	if country != "Netherlands" {
		t.Errorf("country came back as %q", country)
	}
	if code != "NL" {
		t.Errorf("country code came back as %q", code)
	}
	if city != "Amsterdam" {
		t.Errorf("city came back as %q", city)
	}
}

// A relay that could not be placed says so with empty values rather than
// leaving the previous country in place.
func TestAnUnplaceableRelayComesBackEmpty(t *testing.T) {
	country, code, city := parseLocation("countryCode=\ncountry=\n")
	if country != "" || code != "" || city != "" {
		t.Errorf("expected nothing, got %q %q %q", country, code, city)
	}
}

// Anything the script prints that is not a key=value line is not a field.
func TestNoiseInTheReplyIsIgnored(t *testing.T) {
	_, code, _ := parseLocation("Warning: something\ncountryCode=FR\n")
	if code != "FR" {
		t.Errorf("a warning line broke the parse: code=%q", code)
	}
}
