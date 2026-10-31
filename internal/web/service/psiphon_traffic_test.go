package service

import (
	"reflect"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
)

func psiphonTrafficOf(t *testing.T) map[string]psiphon.Traffic {
	t.Helper()
	var rows []model.PsiphonCountry
	if err := database.GetDB().Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]psiphon.Traffic{}
	for _, r := range rows {
		out[r.Code] = psiphon.Traffic{Down: r.TrafficDown, Up: r.TrafficUp}
	}
	return out
}

// Each flush adds to what a country carried before, a country switched off
// since its last bytes included; the card shows the sums, and switching off keeps them.
func TestPsiphonTrafficAddsUpOnEachCountry(t *testing.T) {
	setupRelayDB(t)
	seedPsiphonCountries(t,
		model.PsiphonCountry{Code: "DE", Enabled: true, SocksPort: 11181, HTTPPort: 11381, TrafficDown: 1000, TrafficUp: 10},
		model.PsiphonCountry{Code: "FR", Enabled: false, SocksPort: 11182, HTTPPort: 11382},
	)
	for range 2 {
		if err := addPsiphonTraffic(map[string]psiphon.Traffic{
			"DE": {Down: 2524300, Up: 722},
			"FR": {Down: 300, Up: 3},
			"NL": {Down: 7, Up: 7},
		}); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}
	want := map[string]psiphon.Traffic{"DE": {Down: 1000 + 2*2524300, Up: 10 + 2*722}, "FR": {Down: 600, Up: 6}}
	if got := psiphonTrafficOf(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("after two flushes %v, want %v", got, want)
	}

	svc := &PsiphonService{}
	state, err := svc.State()
	if err != nil {
		t.Fatal(err)
	}
	shown := map[string]psiphon.Traffic{}
	for _, c := range state.Countries {
		shown[c.Code] = psiphon.Traffic{Down: c.TrafficDown, Up: c.TrafficUp}
	}
	if !reflect.DeepEqual(shown, want) {
		t.Fatalf("the card shows %v, want %v", shown, want)
	}

	if err := svc.SetCountries(nil); err != nil {
		t.Fatalf("switching DE off: %v", err)
	}
	if got := psiphonTrafficOf(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("switching DE off left %v, want %v", got, want)
	}
}

// A reset names one country, in any case, or all; one with no row, a body naming neither
// (a misspelt field) and a standby copy are refused rather than reported done.
func TestResettingPsiphonTraffic(t *testing.T) {
	setupRelayDB(t)
	seedPsiphonCountries(t,
		model.PsiphonCountry{Code: "DE", Enabled: true, SocksPort: 11181, HTTPPort: 11381, TrafficDown: 1000, TrafficUp: 10},
		model.PsiphonCountry{Code: "US", Enabled: false, SocksPort: 11182, HTTPPort: 11382, TrafficDown: 2000, TrafficUp: 20},
	)
	svc := &PsiphonService{}
	if err := svc.ResetTraffic("de", false); err != nil {
		t.Fatalf("reset DE: %v", err)
	}
	want := map[string]psiphon.Traffic{"DE": {}, "US": {Down: 2000, Up: 20}}
	if got := psiphonTrafficOf(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("after resetting DE %v, want %v", got, want)
	}

	for _, refused := range []struct {
		code    string
		all     bool
		standby bool
		err     string
	}{
		{"NL", false, false, "there is no Psiphon country NL"},
		{"", false, false, "name one Psiphon country to reset, or all of them"},
		{"US", true, false, "name one Psiphon country to reset, or all of them"},
		{"", true, true, global.ErrStandby.Error()},
	} {
		global.SetStandby(refused.standby)
		err := svc.ResetTraffic(refused.code, refused.all)
		global.SetStandby(false)
		if err == nil || err.Error() != refused.err {
			t.Fatalf("resetting %+v: %v, want %q", refused, err, refused.err)
		}
		if got := psiphonTrafficOf(t); !reflect.DeepEqual(got, want) {
			t.Fatalf("a refused reset %+v left %v, want %v", refused, got, want)
		}
	}

	if err := svc.ResetTraffic("", true); err != nil {
		t.Fatalf("reset all: %v", err)
	}
	if got := psiphonTrafficOf(t); !reflect.DeepEqual(got, map[string]psiphon.Traffic{"DE": {}, "US": {}}) {
		t.Fatalf("after resetting every country %v", got)
	}
}
