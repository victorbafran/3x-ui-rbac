package service

import (
	"slices"
	"testing"
)

// A sub name pointed at a front's IP reaches the subscription server only if
// the front forwards its port; with the sub server off, nothing is opened.
func TestAFrontForwardsTheSubscriptionPortWhileTheSubServerIsOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		on   bool
	}{{"sub server on", true}, {"sub server off", false}} {
		t.Run(tc.name, func(t *testing.T) {
			setupRelayDB(t)
			var settings SettingService
			if err := settings.setBool("subEnable", tc.on); err != nil {
				t.Fatal(err)
			}
			if err := settings.setInt("subPort", 2097); err != nil {
				t.Fatal(err)
			}
			plain, _, err := (&RelayService{}).frontForwardedPorts(3080, 0, 22)
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(plain, 2097); got != tc.on {
				t.Fatalf("forwarded %v: sub port 2097 forwarded = %v, want %v", plain, got, tc.on)
			}
		})
	}
}
