// Package psiphon runs Psiphon's client on the panel's machine, one process per
// country, each a loopback SOCKS port Xray sends a host's customers out through.
package psiphon

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// BinaryCommit pins Psiphon's published client build. Psiphon signs no release,
// so the commit plus BinarySHA256 is the whole integrity check.
const (
	BinaryCommit = "196a723c3f07a35df0e36aa05190ad1cf3922d61"
	BinarySHA256 = "d83dcaa632b265536852e2d97c45eb4831960f8dbcb2d8638293d5958483705b"
)

// FallbackRegions is what Psiphon announced on 2026-10-28, used when a probe
// cannot read the live list.
var FallbackRegions = []string{"AT", "AU", "BE", "BR", "CA", "CH", "CZ", "DE", "DK", "ES", "FR", "GB", "ID", "IE", "IN", "IT", "JP", "LT", "NL", "NO", "PL", "RO", "RS", "SE", "SG", "US"}

// Supported is nil where Psiphon publishes a client: it builds for linux/amd64
// only, and an arm64 build from source was never measured.
func Supported() error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("psiphon publishes its client for linux/amd64 only; this panel runs on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

// Root holds one data directory per country; x-ui's unit may write under the
// DB folder and nowhere else.
func Root() string {
	return filepath.Join(config.GetDBFolderPath(), "psiphon")
}

// clientConfig is the config measured on the panel box. ListenInterface has
// no field on purpose: set at all, the client exits 0 at once and loops.
type clientConfig struct {
	PropagationChannelID               string `json:"PropagationChannelId"`
	SponsorID                          string `json:"SponsorId"`
	RemoteServerListDownloadFilename   string `json:"RemoteServerListDownloadFilename"`
	RemoteServerListSignaturePublicKey string `json:"RemoteServerListSignaturePublicKey"`
	RemoteServerListURL                string `json:"RemoteServerListUrl"`
	UseIndistinguishableTLS            bool   `json:"UseIndistinguishableTLS"`
	EgressRegion                       string `json:"EgressRegion,omitempty"`
	LocalSocksProxyPort                int    `json:"LocalSocksProxyPort"`
	LocalHTTPProxyPort                 int    `json:"LocalHttpProxyPort"`
	// EmitBytesTransferred makes the client print what it carried about once a
	// second while traffic flows: the only count there is, as Xray's is off.
	EmitBytesTransferred bool `json:"EmitBytesTransferred"`
}

const serverListSigningKey = "MIICIDANBgkqhkiG9w0BAQEFAAOCAg0AMIICCAKCAgEAt7Ls+/39r+T6zNW7GiVpJfzq/xvL9SBH5rIFnk0RXYEYavax3WS6HOD35eTAqn8AniOwiH+DOkvgSKF2caqk/y1dfq47Pdymtwzp9ikpB1C5OfAysXzBiwVJlCdajBKvBZDerV1cMvRzCKvKwRmvDmHgphQQ7WfXIGbRbmmk6opMBh3roE42KcotLFtqp0RRwLtcBRNtCdsrVsjiI1Lqz/lH+T61sGjSjQ3CHMuZYSQJZo/KrvzgQXpkaCTdbObxHqb6/+i1qaVOfEsvjoiyzTxJADvSytVtcTjijhPEV6XskJVHE1Zgl+7rATr/pDQkw6DPCNBS1+Y6fy7GstZALQXwEDN/qhQI9kWkHijT8ns+i1vGg00Mk/6J75arLhqcodWsdeG/M/moWgqQAnlZAGVtJI1OgeF5fsPpXu4kctOfuZlGjVZXQNW34aOzm8r8S0eVZitPlbhcPiR4gT/aSMz/wd8lZlzZYsje/Jr8u/YtlwjjreZrGRmG8KMOzukV3lLmMppXFMvl4bxv6YFEmIuTsOhbLTwFgh7KYNjodLj/LsqRVfwz31PgWQFTEPICV7GCvgVlPRxnofqKSjgTWI4mxDhBpVcATvaoBl1L/6WLbFvBsoAUBItWwctO2xalKxF5szhGm8lccoc5MZr8kfE0uxMgsxz4er68iCID+rsCAQM="

var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)

// ConfigJSON is one client's config; region "" lets Psiphon pick, which is
// how the probe reads the country list.
func ConfigJSON(region string, socksPort, httpPort int) ([]byte, error) {
	if region != "" && !countryCode.MatchString(region) {
		return nil, fmt.Errorf("psiphon country %q is not a two-letter upper-case code", region)
	}
	for _, port := range []int{socksPort, httpPort} {
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("psiphon port %d is out of range", port)
		}
	}
	if socksPort == httpPort {
		return nil, errors.New("psiphon's SOCKS and HTTP ports must differ")
	}
	return json.Marshal(clientConfig{
		PropagationChannelID:               "FFFFFFFFFFFFFFFF",
		SponsorID:                          "FFFFFFFFFFFFFFFF",
		RemoteServerListDownloadFilename:   "remote_server_list",
		RemoteServerListSignaturePublicKey: serverListSigningKey,
		// The doubled slash is in the URL that was measured working.
		RemoteServerListURL:     "https://s3.amazonaws.com//psiphon/web/mjr4-p23r-puwl/server_list_compressed",
		UseIndistinguishableTLS: true,
		EgressRegion:            region,
		LocalSocksProxyPort:     socksPort,
		LocalHTTPProxyPort:      httpPort,
		EmitBytesTransferred:    true,
	})
}

// Notice is one line of the client's output: a JSON object per line.
type Notice struct {
	Type string
	Data json.RawMessage
}

// ParseNotice reads one output line, and is false for anything that is not a
// notice, such as a Go panic on its way out.
func ParseNotice(line string) (Notice, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return Notice{}, false
	}
	var raw struct {
		NoticeType string          `json:"noticeType"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil || raw.NoticeType == "" {
		return Notice{}, false
	}
	return Notice{Type: raw.NoticeType, Data: raw.Data}, true
}

// Regions is the country list an AvailableEgressRegions notice carries, sorted,
// keeping only codes safe to use as a directory name and an Xray tag.
func Regions(n Notice) []string {
	if n.Type != "AvailableEgressRegions" {
		return nil
	}
	var data struct {
		Regions []string `json:"regions"`
	}
	if json.Unmarshal(n.Data, &data) != nil {
		return nil
	}
	var out []string
	for _, code := range data.Regions {
		if countryCode.MatchString(code) && !slices.Contains(out, code) {
			out = append(out, code)
		}
	}
	slices.Sort(out)
	return out
}

// Connected reads a Tunnels notice: whether any tunnel is up, and whether the
// notice was a Tunnels one at all.
func Connected(n Notice) (bool, bool) {
	if n.Type != "Tunnels" {
		return false, false
	}
	var data struct {
		Count *int `json:"count"`
	}
	if json.Unmarshal(n.Data, &data) != nil || data.Count == nil {
		return false, false
	}
	return *data.Count > 0, true
}

// serverRegion is the country the latest tunnel landed in.
func serverRegion(n Notice) string {
	if n.Type != "ConnectedServerRegion" {
		return ""
	}
	var data struct {
		ServerRegion string `json:"serverRegion"`
	}
	if json.Unmarshal(n.Data, &data) != nil {
		return ""
	}
	return data.ServerRegion
}

// bytesTransferred reads a BytesTransferred notice: what the client carried
// since its previous one, received being what customers downloaded.
func bytesTransferred(n Notice) (Traffic, bool) {
	if n.Type != "BytesTransferred" {
		return Traffic{}, false
	}
	var data struct {
		Received int64 `json:"received"`
		Sent     int64 `json:"sent"`
	}
	if json.Unmarshal(n.Data, &data) != nil {
		return Traffic{}, false
	}
	return Traffic{Down: data.Received, Up: data.Sent}, true
}

// chattyNotices repeat on every handshake and say nothing an operator acts
// on; Tunnels, the region, port and proxy errors and exits stay visible.
var chattyNotices = []string{
	"ApplicationParameters", "ServerTimestamp", "ActiveAuthorizationIDs", "TrafficRateLimits",
	"ClientRegion", "ClientAddress", "NetworkID", "Homepage", "ClientUpgradeAvailable",
	"ClientIsLatestVersion", "AvailableEgressRegions", "SplitTunnelRegions", "SkipServerEntry",
	"LightProxyAvailable", "DSLAccessTokenAvailable",
}

// noticeNoise folds the chatty notices and drops BytesTransferred outright: a busy
// country prints it every second, read for its traffic and never worth a line.
var noticeNoise = func() []sidecar.NoiseRule {
	rules := make([]sidecar.NoiseRule, 0, len(chattyNotices)+1)
	for _, kind := range chattyNotices {
		rules = append(rules, sidecar.NoiseRule{Kind: kind, Line: noticeOf(kind)})
	}
	return append(rules, sidecar.NoiseRule{Kind: "BytesTransferred", Line: noticeOf("BytesTransferred"), Silent: true})
}()

func noticeOf(kind string) *regexp.Regexp { return regexp.MustCompile(`"noticeType":"` + kind + `"`) }
