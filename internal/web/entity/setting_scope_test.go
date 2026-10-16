package entity

import (
	"reflect"
	"testing"
)

// fillNonZero gives every settable field a non-zero value so a field that
// survives ForReseller is visible as a leak.
func fillNonZero(t *testing.T, v reflect.Value) {
	t.Helper()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Kind() {
		case reflect.String:
			f.SetString("leaked")
		case reflect.Int, reflect.Int64, reflect.Int32:
			f.SetInt(42)
		case reflect.Bool:
			f.SetBool(true)
		}
	}
}

// Every field outside the allowlist must come back zero -- including fields
// added to AllSetting later, which is the whole point of allowlisting. If this
// fails after a rebase, a new upstream setting needs a deliberate decision.
func TestForResellerDropsEverythingNotAllowlisted(t *testing.T) {
	full := &AllSettingView{}
	fillNonZero(t, reflect.ValueOf(&full.AllSetting).Elem())
	fillNonZero(t, reflect.ValueOf(full).Elem())

	got := full.ForReseller()
	v := reflect.ValueOf(got.AllSetting)
	ty := v.Type()
	for i := 0; i < ty.NumField(); i++ {
		name := ty.Field(i).Name
		isZero := v.Field(i).IsZero()
		if resellerVisibleSettings[name] && isZero {
			t.Errorf("%s is allowlisted but came back empty", name)
		}
		if !resellerVisibleSettings[name] && !isZero {
			t.Errorf("%s is not allowlisted but leaked value %v", name, v.Field(i).Interface())
		}
	}
}

// The Has* flags say whether a secret is configured. That is still panel
// infrastructure, not a reseller's business.
func TestForResellerDropsSecretFlags(t *testing.T) {
	full := &AllSettingView{}
	fillNonZero(t, reflect.ValueOf(full).Elem())
	got := full.ForReseller()
	if got.HasTgBotToken || got.HasSmtpPassword || got.HasLdapPassword ||
		got.HasDiscordBotToken || got.HasTwoFactorToken || got.HasApiToken ||
		got.HasWarpSecret || got.HasNordSecret {
		t.Fatalf("secret-configured flags leaked: %+v", got)
	}
}

// An allowlist naming a field that no longer exists reads as coverage for a
// setting nobody is actually protecting.
func TestAllowlistNamesOnlyRealFields(t *testing.T) {
	ty := reflect.TypeOf(AllSetting{})
	real := make(map[string]bool, ty.NumField())
	for i := 0; i < ty.NumField(); i++ {
		real[ty.Field(i).Name] = true
	}
	for name := range resellerVisibleSettings {
		if !real[name] {
			t.Errorf("allowlist names %q, which is not a field of AllSetting", name)
		}
	}
}
