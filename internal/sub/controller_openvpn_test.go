package sub

import (
	"strconv"
	"strings"
	"testing"
)

// The file hangs off the subscription URL the operator configured, not off
// whatever host this one request happened to arrive on. A customer behind a
// reverse proxy would otherwise be handed a link to the panel's raw port.
func TestProfileURLsFollowTheSubscriptionURL(t *testing.T) {
	got := withProfileURLs([]OpenVPNProfile{
		{InboundId: 7, FileName: "Germany.ovpn"},
	}, "https://vpn.example.com/sub/abc123/")

	if len(got) != 1 {
		t.Fatalf("got %d profiles, want 1", len(got))
	}
	want := "https://vpn.example.com/sub/abc123/openvpn/7.ovpn"
	if got[0].URL != want {
		t.Fatalf("url = %q, want %q", got[0].URL, want)
	}
}

// The id is what the URL carries, because two inbounds an operator named the
// same produce one file name between them. A URL built from the name would
// answer both with one country.
func TestProfileURLsAreToldApartByIdNotByName(t *testing.T) {
	got := withProfileURLs([]OpenVPNProfile{
		{InboundId: 1, FileName: "Germany.ovpn"},
		{InboundId: 2, FileName: "Germany.ovpn"},
	}, "https://vpn.example.com/sub/abc123")

	if got[0].URL == got[1].URL {
		t.Fatalf("both countries are at %q", got[0].URL)
	}
	for _, p := range got {
		if !strings.HasSuffix(p.URL, "/openvpn/"+strconv.Itoa(p.InboundId)+".ovpn") {
			t.Errorf("url %q does not name inbound %d", p.URL, p.InboundId)
		}
	}
}

// Without a subscription URL there is nothing to hang the file off, and a
// relative link would resolve against whatever page the customer is on.
func TestProfilesGetNoURLWhenThereIsNoSubscriptionURL(t *testing.T) {
	got := withProfileURLs([]OpenVPNProfile{{InboundId: 7}}, "")
	if len(got) != 1 {
		t.Fatalf("got %d profiles, want 1", len(got))
	}
	if got[0].URL != "" {
		t.Fatalf("url = %q, want it left empty so the page can hide the button", got[0].URL)
	}
}
