package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A front is located by its own address. The services are asked about that
// address, not about whoever is asking, and the first that answers wins.
func TestAFrontIsLocatedByItsOwnAddress(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/down/"):
			http.Error(w, "busy", http.StatusTooManyRequests)
		case strings.HasPrefix(r.URL.Path, "/up/"):
			_, _ = w.Write([]byte(`{"ip":"185.226.116.154","country":"Iran","country_code":"IR","city":"Tehran"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	saved := addressSources
	addressSources = []string{srv.URL + "/down/%s", srv.URL + "/up/%s"}
	t.Cleanup(func() { addressSources = saved })

	h, err := WhereIs(t.Context(), "185.226.116.154")
	if err != nil {
		t.Fatal(err)
	}
	if h.CountryCode != "IR" || h.Country != "Iran" || h.City != "Tehran" {
		t.Fatalf("got %+v", h)
	}
	if len(asked) != 2 || asked[0] != "/down/185.226.116.154" || asked[1] != "/up/185.226.116.154" {
		t.Fatalf("the services were not asked about the front's address: %v", asked)
	}
}

// Nothing answering is an error, never a guessed country.
func TestNoAnswerIsAnErrorNotAGuess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	defer srv.Close()
	saved := addressSources
	addressSources = []string{srv.URL + "/%s"}
	t.Cleanup(func() { addressSources = saved })

	if h, err := WhereIs(t.Context(), "203.0.113.9"); err == nil {
		t.Fatalf("an empty answer became a location: %+v", h)
	}
}

func TestAnAddressIsLookedUpAsItsIP(t *testing.T) {
	for in, want := range map[string]string{"185.226.116.154": "185.226.116.154", " [2001:db8::7] ": "2001:db8::7"} {
		got, err := resolveForLookup(t.Context(), in)
		if err != nil || got != want {
			t.Errorf("resolveForLookup(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := resolveForLookup(t.Context(), "  "); err == nil {
		t.Error("an empty address was looked up")
	}
}
