package version

import "testing"

func TestCompareRejectsUnexpectedFormats(t *testing.T) {
	if _, ok := Compare("latest", "2.9.3"); ok {
		t.Fatal("expected non-semver latest tag to be rejected")
	}
	if _, ok := Compare("v2.9", "2.9.3"); ok {
		t.Fatal("expected short version to be rejected")
	}
}

// The updater must see rbac42 as newer than rbac41 on the same base, and a new
// base as newer whatever the fork number, or it offers the wrong release.
func TestCompareReleaseReadsTheForkNumber(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v3.10.2-rbac42", "v3.10.2-rbac41", 1, true},
		{"v3.10.2-rbac9", "v3.10.2-rbac10", -1, true},
		{"v3.10.2-rbac41", "3.10.2-rbac41", 0, true},
		{"v3.11.0-rbac1", "v3.10.2-rbac99", 1, true},
		{"v3.10.2-rbac1", "3.10.2", 1, true},
		{"3.10.2", "v3.10.2", 0, true},
		{"v3.11.0", "v3.10.2-rbac41", 1, true},
		{"v3.10.2-rbac", "v3.10.2", 0, false},
		{"v3.10.2-42", "v3.10.2", 0, false},
		{"dev-latest", "v3.10.2", 0, false},
	} {
		got, ok := CompareRelease(tc.a, tc.b)
		if got != tc.want || ok != tc.ok {
			t.Errorf("CompareRelease(%q, %q) = %d, %v; want %d, %v", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
}
