package tunnel

import "testing"

// A direct inbound is exactly what it was before fronts existed. Changing
// anything here would silently alter every cisco inbound already running.
func TestADirectInboundIsLeftCompletelyAlone(t *testing.T) {
	operator := CiscoDefaults{MTU: 0, MaxSameClients: 0, MobileDPD: 0, DisableUDP: false}
	got := CiscoBehind(false, operator)
	if got != operator {
		t.Fatalf("a direct inbound was changed: %+v", got)
	}
}

// Behind a front, the three measured settings are applied so the operator
// does not have to know them. Each was set by hand the first time, and the
// whole point of this is that they no longer are.
func TestBehindAFrontTheMeasuredDefaultsAreApplied(t *testing.T) {
	got := CiscoBehind(true, CiscoDefaults{})

	if got.MTU != 1300 {
		t.Errorf("MTU = %d, want 1300: the front's encapsulation pushes 1400 past the wire limit", got.MTU)
	}
	if got.MaxSameClients != 5 {
		t.Errorf("MaxSameClients = %d, want 5: a phone behind a front holds several ghost sessions", got.MaxSameClients)
	}
	if got.MobileDPD != 90 {
		t.Errorf("MobileDPD = %d, want 90: thirty minutes of ghosts fills the account", got.MobileDPD)
	}
	// DTLS stays on: the common phone is on one network and pays nothing,
	// and turning it off would cost everyone speed to spare the roamers.
	if got.DisableUDP {
		t.Error("DTLS was forced off; that trades the majority's speed for the roamers' reconnect")
	}
}

// The defaults are for the common case, not a cage. An operator who set a
// value keeps it, behind a front or not.
func TestTheOperatorsOwnValuesAlwaysWin(t *testing.T) {
	operator := CiscoDefaults{MTU: 1200, MaxSameClients: 3, MobileDPD: 300, DisableUDP: true}
	got := CiscoBehind(true, operator)
	if got != operator {
		t.Fatalf("the operator's settings were overridden: got %+v, want %+v", got, operator)
	}
}

// Each field falls back on its own. An operator who set only the MTU still
// gets the device and DPD defaults, not a half-configured inbound.
func TestEachUnsetFieldFallsBackOnItsOwn(t *testing.T) {
	got := CiscoBehind(true, CiscoDefaults{MTU: 1250})
	if got.MTU != 1250 {
		t.Errorf("MTU = %d, want the operator's 1250", got.MTU)
	}
	if got.MaxSameClients != 5 {
		t.Errorf("MaxSameClients = %d, want the default 5", got.MaxSameClients)
	}
	if got.MobileDPD != 90 {
		t.Errorf("MobileDPD = %d, want the default 90", got.MobileDPD)
	}
}
