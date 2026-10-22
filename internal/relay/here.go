package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A cisco country can also leave through the panel's own server, and that
// country deserves its name and flag as much as any relay's. The difference
// is only that nobody has to be reached over SSH to ask: the panel asks for
// itself.

// Here is where the panel's own server turned out to be.
type Here struct {
	Country     string `json:"country"`
	CountryCode string `json:"countryCode"`
	City        string `json:"city"`
}

// Known reports whether the lookup ever produced an answer. An unknown
// location is shown as the two name boxes rather than as a guess: a country
// labelled wrongly is worse than one the operator typed.
func (h Here) Known() bool { return strings.TrimSpace(h.CountryCode) != "" }

// hereTTL is how long an answer is kept. A server does not move, so this is
// only here to pick up an address change — and to avoid asking again on every
// form the operator opens.
const hereTTL = 24 * time.Hour

var here struct {
	sync.Mutex
	value Here
	at    time.Time
}

// lookupTimeout keeps a form from waiting on a service that is not answering.
const lookupTimeout = 8 * time.Second

// sources are asked in order and the first that answers wins. ipinfo refuses
// plenty of datacentre addresses outright — measured, it 403s two of the
// owner's three servers — so it cannot be the only one asked.
var sources = []string{
	"https://ipwho.is/",
	"https://ipinfo.io/json",
	"http://ip-api.com/json/",
}

// WhereAmI reports the panel server's own location, asking at most once a day.
//
// It never returns an error: a panel that cannot say which country it is in
// is still a working panel, and the caller shows the manual fields instead.
func WhereAmI(ctx context.Context) Here {
	here.Lock()
	if here.value.Known() && time.Since(here.at) < hereTTL {
		v := here.value
		here.Unlock()
		return v
	}
	here.Unlock()

	found := askSources(ctx)

	here.Lock()
	defer here.Unlock()
	// Only a real answer replaces a real answer: a service that is down for an
	// hour must not blank out a country the panel already knows.
	if found.Known() || !here.value.Known() {
		here.value = found
		here.at = time.Now()
	}
	return here.value
}

// KnownHere is the cached answer and never a lookup, for a subscription that
// must not wait on one; an unknown or stale answer is refreshed in the background.
func KnownHere() (Here, bool) {
	here.Lock()
	v, fresh := here.value, time.Since(here.at) < hereTTL
	here.Unlock()
	if !fresh && hereRefreshing.CompareAndSwap(false, true) {
		go func() {
			defer hereRefreshing.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), 3*lookupTimeout)
			defer cancel()
			WhereAmI(ctx)
		}()
	}
	return v, v.Known()
}

var hereRefreshing atomic.Bool

// ForgetWhereAmI drops the cached answer, for tests and for an operator who
// has just moved the panel.
func ForgetWhereAmI() {
	here.Lock()
	defer here.Unlock()
	here.value = Here{}
	here.at = time.Time{}
}

func askSources(ctx context.Context) Here {
	client := &http.Client{Timeout: lookupTimeout}
	for _, url := range sources {
		if h, ok := askOne(ctx, client, url); ok {
			return h
		}
	}
	return Here{}
}

// askOne reads one service. The three disagree on field names, so each
// alternative is tried: country_code and countryCode for the code, and a
// "country" that is itself two letters is a code rather than a name.
func askOne(ctx context.Context, client *http.Client, url string) (Here, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Here{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return Here{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Here{}, false
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Here{}, false
	}
	return fromFields(body)
}

func fromFields(body map[string]any) (Here, bool) {
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := body[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	h := Here{
		Country:     str("country"),
		CountryCode: str("country_code", "countryCode"),
		City:        str("city"),
	}
	// ipinfo answers "country":"DE" where the others answer "country":"Germany".
	if h.CountryCode == "" && len(h.Country) == 2 {
		h.CountryCode, h.Country = strings.ToUpper(h.Country), ""
	}
	if !h.Known() {
		return Here{}, false
	}
	return h, true
}
