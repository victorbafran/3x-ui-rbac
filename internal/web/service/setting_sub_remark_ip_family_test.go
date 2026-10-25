package service

import (
	"strings"
	"testing"
)

// Every subscription request reads the marker through the getter, so an unset
// or unknown stored value must read as today's " · IPv6" / " · IPv4".
func TestSubRemarkIPFamilyDefaultsAndPersists(t *testing.T) {
	setupSettingTestDB(t)
	s := &SettingService{}

	if got, err := s.GetSubRemarkIPFamily(); err != nil || got != "version" {
		t.Fatalf("missing setting = %q, %v; want version, nil", got, err)
	}
	settings, err := s.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	if settings.SubRemarkIPFamily != "version" {
		t.Fatalf("the settings page gets %q for a missing setting; want version selected", settings.SubRemarkIPFamily)
	}

	for _, v := range []string{"number", "off", "version"} {
		settings.SubRemarkIPFamily = v
		if err := s.UpdateAllSetting(settings, SecretClears{}); err != nil {
			t.Fatalf("saving %q: %v", v, err)
		}
		if got, err := s.GetSubRemarkIPFamily(); err != nil || got != v {
			t.Fatalf("after saving %q the getter reads %q, %v", v, got, err)
		}
	}

	settings.SubRemarkIPFamily = "letters"
	if err := s.UpdateAllSetting(settings, SecretClears{}); err == nil || !strings.Contains(err.Error(), "subRemarkIPFamily") {
		t.Fatalf("saving letters got %v; want it refused", err)
	}
	// A database restored from a panel that knows a fourth value.
	if err := s.saveSetting("subRemarkIPFamily", "letters"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSubRemarkIPFamily(); err != nil || got != "version" {
		t.Fatalf("a stored unknown value reads %q, %v; want version", got, err)
	}
	// The page shows what subscriptions use, and saving any tab must not trip on it.
	restored, err := s.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	if restored.SubRemarkIPFamily != "version" {
		t.Fatalf("the settings page gets %q for a stored unknown value; want version", restored.SubRemarkIPFamily)
	}
	if err := s.UpdateAllSetting(restored, SecretClears{}); err != nil {
		t.Fatalf("saving the settings as the page got them: %v", err)
	}
}
