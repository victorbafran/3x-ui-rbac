package service

import (
	"strings"
	"testing"
	"time"
)

// Several daily swap times are kept sorted and once each, whatever comma or spacing was typed;
// a time that is not HH:MM, or more times than Arvan's IPs would stand, is refused.
func TestDailySwapTimesAreReadAsAList(t *testing.T) {
	for _, tc := range []struct {
		raw, want, err string
	}{
		{raw: "", want: ""},
		{raw: "  ", want: ""},
		{raw: "04:30", want: "04:30"},
		{raw: "16:30,04:30", want: "04:30, 16:30"},
		{raw: " 12:00 ،  04:30 , 12:00 ", want: "04:30, 12:00"},
		{raw: "4:30 20:15", want: "04:30, 20:15"},
		{raw: "04:30, 25:00", err: `"25:00" is not HH:MM`},
		{raw: "04:30,,noon", err: `"noon" is not HH:MM`},
		{raw: "01:00,02:00,03:00,04:00,05:00,06:00", want: "01:00, 02:00, 03:00, 04:00, 05:00, 06:00"},
		{raw: "01:00,02:00,03:00,04:00,05:00,06:00,07:00", err: "too many"},
	} {
		got, err := normalizeDailySwapAt(tc.raw)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: got %q, %v; want an error with %q", tc.raw, got, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v; want %q", tc.raw, got, err, tc.want)
		}
	}
}

// Each time fires once within the hour after it and not again once a swap has run since;
// a time already past by more than the hour, or still to come, does not fire.
func TestEachDailySwapTimeFiresOnce(t *testing.T) {
	loc := time.FixedZone("Tehran", 3*3600+1800)
	day := func(hh, mm int) time.Time { return time.Date(2026, 11, 26, hh, mm, 0, 0, loc) }
	const times = "04:30, 12:00, 20:00"
	for _, tc := range []struct {
		name string
		now  time.Time
		last time.Time
		want bool
	}{
		{"the first time has come", day(4, 31), day(1, 0), true},
		{"the first time, swapped already", day(4, 40), day(4, 32), false},
		{"between the times", day(9, 0), day(4, 32), false},
		{"the second time has come", day(12, 5), day(4, 32), true},
		{"the second time, missed by more than the hour", day(13, 30), day(4, 32), false},
		{"the third time has come", day(20, 0), day(12, 6), true},
		{"before the first time", day(3, 0), day(0, 0), false},
		{"never swapped before", day(20, 10), time.Unix(0, 0), true},
	} {
		if got := dailySwapDue(tc.now, times, tc.last.Unix(), loc); got != tc.want {
			t.Errorf("%s: due %v, want %v", tc.name, got, tc.want)
		}
	}
	if dailySwapDue(day(4, 31), "", 0, loc) {
		t.Error("no time set still fired")
	}
	if !dailySwapDue(day(4, 31), "04:30", 0, loc) {
		t.Error("a single time as saved before did not fire")
	}
}
