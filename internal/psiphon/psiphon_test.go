package psiphon

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// Lines as the client printed them on the panel box (2026-10-28).
const (
	regionsLine      = `{"data":{"regions":["AT","AU","BE","BR","CA","CH","CZ","DE","DK","ES","FR","GB","ID","IE","IN","IT","JP","LT","NL","NO","PL","RO","RS","SE","SG","US"]},"noticeType":"AvailableEgressRegions","timestamp":"2026-10-28T15:04:05.123Z"}`
	firstStartLine   = `{"data":{"regions":[]},"noticeType":"AvailableEgressRegions","timestamp":"2026-10-28T15:04:01.042Z"}`
	tunnelUpLine     = `{"data":{"count":1},"noticeType":"Tunnels","timestamp":"2026-10-28T15:04:07.311Z"}`
	tunnelDownLine   = `{"data":{"count":0},"noticeType":"Tunnels","timestamp":"2026-10-28T15:09:12.500Z"}`
	serverRegionLine = `{"data":{"serverRegion":"DE"},"noticeType":"ConnectedServerRegion","timestamp":"2026-10-28T15:04:07.309Z"}`
)

// With ListenInterface set at all the client exits 0 at once and loops, and
// the measured server-list URL keeps its doubled slash.
func TestConfigIsTheMeasuredShapeAndNeverSetsListenInterface(t *testing.T) {
	raw, err := ConfigJSON("DE", 11181, 11381)
	if err != nil {
		t.Fatalf("ConfigJSON: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	if _, set := cfg["ListenInterface"]; set {
		t.Fatalf("ListenInterface is set; the client would exit at once and loop:\n%s", raw)
	}
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{
		"EgressRegion", "LocalHttpProxyPort", "LocalSocksProxyPort", "PropagationChannelId", "RemoteServerListDownloadFilename",
		"RemoteServerListSignaturePublicKey", "RemoteServerListUrl", "SponsorId", "UseIndistinguishableTLS",
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("config keys %v, want the measured %v", keys, want)
	}
	for key, value := range map[string]any{
		"EgressRegion": "DE", "LocalSocksProxyPort": 11181.0, "LocalHttpProxyPort": 11381.0, "UseIndistinguishableTLS": true,
		"RemoteServerListUrl": "https://s3.amazonaws.com//psiphon/web/mjr4-p23r-puwl/server_list_compressed",
	} {
		if cfg[key] != value {
			t.Errorf("%s = %v, want %v", key, cfg[key], value)
		}
	}
	if key, _ := cfg["RemoteServerListSignaturePublicKey"].(string); !strings.HasPrefix(key, "MIICIDANBgkqhkiG9w0BAQEFAAOCAg0A") || len(key) != 732 {
		t.Errorf("the server-list signing key is not the 4096-bit one measured (%d chars)", len(key))
	}

	probe, err := ConfigJSON("", 11179, 11379)
	if err != nil {
		t.Fatalf("probe config: %v", err)
	}
	if strings.Contains(string(probe), "EgressRegion") {
		t.Fatalf("the probe is pinned to a country, so it would list only that one:\n%s", probe)
	}
}

func TestConfigRefusesWhatTheClientCannotUse(t *testing.T) {
	for _, tc := range []struct {
		region      string
		socks, http int
		want        string
	}{
		{"de", 11181, 11381, "two-letter"},
		{"../DE", 11181, 11381, "two-letter"},
		{"DE", 0, 11381, "out of range"},
		{"DE", 11181, 70000, "out of range"},
		{"DE", 11181, 11181, "must differ"},
	} {
		if _, err := ConfigJSON(tc.region, tc.socks, tc.http); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ConfigJSON(%q, %d, %d) = %v, want an error saying %q", tc.region, tc.socks, tc.http, err, tc.want)
		}
	}
}

func TestNoticesAreReadFromTheClientsOwnLines(t *testing.T) {
	for _, line := range []string{"panic: runtime error: index out of range", `{"data":{"count":1}}`, `{"noticeType":`, ""} {
		if n, ok := ParseNotice(line); ok {
			t.Errorf("ParseNotice(%q) = %+v, want no notice", line, n)
		}
	}

	n, ok := ParseNotice(regionsLine + "\r")
	if !ok || n.Type != "AvailableEgressRegions" {
		t.Fatalf("ParseNotice(regions) = %+v, %v", n, ok)
	}
	if got := Regions(n); !slices.Equal(got, FallbackRegions) {
		t.Fatalf("Regions = %v, want the 26 listed", got)
	}
	first, _ := ParseNotice(firstStartLine)
	if got := Regions(first); len(got) != 0 {
		t.Fatalf("a first start's empty list read as %v", got)
	}
	messy, _ := ParseNotice(`{"data":{"regions":["US","de","DE","../x","","US","AT"]},"noticeType":"AvailableEgressRegions"}`)
	if got := Regions(messy); !slices.Equal(got, []string{"AT", "DE", "US"}) {
		t.Fatalf("Regions kept codes that are not countries or repeated: %v", got)
	}
	up, _ := ParseNotice(tunnelUpLine)
	if got := Regions(up); got != nil {
		t.Fatalf("a Tunnels notice gave regions %v", got)
	}

	for _, tc := range []struct {
		line             string
		connected, isOne bool
	}{
		{tunnelUpLine, true, true},
		{tunnelDownLine, false, true},
		{serverRegionLine, false, false},
		{`{"data":{},"noticeType":"Tunnels"}`, false, false},
	} {
		n, _ := ParseNotice(tc.line)
		if connected, isOne := Connected(n); connected != tc.connected || isOne != tc.isOne {
			t.Errorf("Connected(%s) = %v, %v; want %v, %v", tc.line, connected, isOne, tc.connected, tc.isOne)
		}
	}
	region, _ := ParseNotice(serverRegionLine)
	if got := serverRegion(region); got != "DE" {
		t.Fatalf("serverRegion = %q", got)
	}
}

// Every handshake repeats a dozen notices; the tunnel count, the region, a
// port clash and lines that are not notices must stay in the panel log.
func TestOnlyTheChattyNoticesAreFolded(t *testing.T) {
	folded := func(line string) bool {
		for _, r := range noticeNoise {
			if r.Line.MatchString(line) {
				return true
			}
		}
		return false
	}
	for _, line := range []string{
		regionsLine,
		`{"data":{"timestamp":"2026-10-28T15:04:07Z"},"noticeType":"ServerTimestamp","timestamp":"2026-10-28T15:04:07.400Z"}`,
		`{"data":{"ApplicationParameters":{}},"noticeType":"ApplicationParameters","timestamp":"2026-10-28T15:04:07.401Z"}`,
	} {
		if !folded(line) {
			t.Errorf("not folded: %s", line)
		}
	}
	for _, line := range []string{
		tunnelUpLine, serverRegionLine,
		`{"data":{"port":11181},"noticeType":"SocksProxyPortInUse","timestamp":"2026-10-28T15:04:01.100Z"}`,
		`{"data":{},"noticeType":"Exiting","timestamp":"2026-10-28T15:09:00.000Z"}`,
		"panic: runtime error: index out of range",
	} {
		if folded(line) {
			t.Errorf("folded out of the log: %s", line)
		}
	}
}
