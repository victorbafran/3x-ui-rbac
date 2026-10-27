package arvan

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

type reply struct {
	status int
	body   string
}

type request struct {
	method, target, auth, ctype string
	body                        []byte
}

type replayServer struct {
	mu   sync.Mutex
	seen []request
}

func (s *replayServer) requests() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]request(nil), s.seen...)
}

// replay answers "METHOD /path?query" with canned replies; any other request
// fails the test, so a call the client should not make is caught.
func replay(t *testing.T, key string, routes map[string]reply) (*Client, *replayServer) {
	t.Helper()
	rs := &replayServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		target := r.URL.Path
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		rs.mu.Lock()
		rs.seen = append(rs.seen, request{
			method: r.Method, target: target, auth: r.Header.Get("Authorization"),
			ctype: r.Header.Get("Content-Type"), body: body,
		})
		rs.mu.Unlock()
		rep, ok := routes[r.Method+" "+target]
		if !ok {
			t.Errorf("unexpected request %s %s", r.Method, target)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		if rep.status == 0 {
			rep.status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	old := Base
	Base = srv.URL
	c := New(key, srv.Client())
	Base = old
	return c, rs
}

const (
	sampleRegions = `{"data":[
		{"code":"ir-thr-ba1","country":"Iran","city":"Tehran","dc":"Bamdad","create":true,"visible":true},
		{"code":"ir-tbz-sh1","country":"Iran","city":"Tabriz","dc":"Shahriar","create":true,"visible":true},
		{"code":"eu-west1-a","country":"Netherlands","city":"Amsterdam","dc":"Amsterdam","create":false,"visible":false}]}`
	sampleServer = `{"id":"8e3a1111","name":"ubuntu - 1 vCPU - 1 GB RAM","status":"ACTIVE",
		"addresses":{"public206":[{"mac_addr":"fa:16:3e:00:89:74","version":"4","addr":"37.152.191.4","type":"fixed","is_public":true,"is_vpc":false}]},
		"security_groups":[{"id":"1d374ba1-sg","name":"Default"}]}`
	sampleServers = `{"data":[` + sampleServer + `]}`
	samplePorts   = `{"data":[
		{"id":"32de7eee-p","network_id":"4cdd-net","status":"ACTIVE","mac_address":"fa:16:3e:00:89:74","ips":["37.152.191.4"],"instance_id":"8e3a1111","security_group_ids":["1d374ba1-sg"],"port_security_enabled":true},
		{"id":"7dee-p","network_id":"4cdd-net","status":"ACTIVE","mac_address":"fa:16:3e:41:45:71","ips":["188.213.196.86"],"instance_id":"8e3a1111","security_group_ids":["1d374ba1-sg"],"port_security_enabled":true}]}`
	sampleAddIP  = `{"data":{"fixed_ips":[{"subnet_id":"3601-sub","ip_address":"188.213.196.86"}],"port_id":"7dee-p","mac_addr":"fa:16:3e:41:45:71"},"message":"New IP added to server successfully."}`
	sampleRecord = `{"id":"38a9-rec","type":"a","name":"bemanche","value":[{"ip":"37.152.191.4","port":80,"weight":100,"country":""}],
		"ttl":120,"cloud":false,"upstream_https":"http","ip_filter_mode":{"count":"single","order":"none","geo_filter":"none"},
		"is_protected":false,"usage":[],"created_at":"2026-10-20T08:00:00Z","updated_at":"2026-10-21T08:00:00Z"}`
	sampleCNAME = `{"id":"c1-rec","type":"cname","name":"www","value":{"host":"aliwoodbaf.ir","host_header":"source"},"ttl":120,"cloud":true}`
)

func TestNewNormalisesTheKeyPrefix(t *testing.T) {
	cases := []struct{ pasted, header string }{
		{"apikey 1fd9abc", "apikey 1fd9abc"},
		{"1fd9abc", "apikey 1fd9abc"},
		{"  ApiKey   1fd9abc\n", "apikey 1fd9abc"},
	}
	for _, tc := range cases {
		t.Run(tc.pasted, func(t *testing.T) {
			c, rs := replay(t, tc.pasted, map[string]reply{"GET /ecc/v1/regions": {body: `{"data":[]}`}})
			if _, err := c.Zones(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := rs.requests()[0].auth; got != tc.header {
				t.Fatalf("Authorization = %q, want %q", got, tc.header)
			}
		})
	}
}

func TestServersDecodeTheSample(t *testing.T) {
	want := Server{
		ID: "8e3a1111", Name: "ubuntu - 1 vCPU - 1 GB RAM", Status: "ACTIVE", Zone: "ir-thr-ba1",
		Addresses:        []Address{{IP: "37.152.191.4", MAC: "fa:16:3e:00:89:74", Network: "public206", Version: "4", Public: true}},
		SecurityGroupIDs: []string{"1d374ba1-sg"},
	}
	cases := []struct {
		name, server string
	}{
		{"version as a string, as measured", sampleServer},
		{"version as a number", strings.Replace(sampleServer, `"version":"4"`, `"version":4`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := replay(t, "k", map[string]reply{
				"GET /ecc/v1/regions/ir-thr-ba1/servers":          {body: `{"data":[` + tc.server + `]}`},
				"GET /ecc/v1/regions/ir-thr-ba1/servers/8e3a1111": {body: `{"data":` + tc.server + `}`},
			})
			list, err := c.Servers(context.Background(), "ir-thr-ba1")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(list, []Server{want}) {
				t.Fatalf("Servers = %+v, want %+v", list, want)
			}
			one, err := c.Server(context.Background(), "ir-thr-ba1", "8e3a1111")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(one, want) {
				t.Fatalf("Server = %+v, want %+v", one, want)
			}
		})
	}
}

func TestFindServerByIP(t *testing.T) {
	refused := reply{status: http.StatusForbidden, body: `{"message":"You don't have enough permissions for this action"}`}
	cases := []struct {
		name     string
		tabriz   reply
		ip       string
		wantZone string
		wantAPI  int
	}{
		{"found in its zone", refused, "37.152.191.4", "ir-thr-ba1", 0},
		{"nowhere, and a zone refused", refused, "9.9.9.9", "", http.StatusForbidden},
		{"nowhere", reply{body: `{"data":[]}`}, "9.9.9.9", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := replay(t, "k", map[string]reply{
				"GET /ecc/v1/regions":                    {body: sampleRegions},
				"GET /ecc/v1/regions/ir-thr-ba1/servers": {body: sampleServers},
				"GET /ecc/v1/regions/ir-tbz-sh1/servers": tc.tabriz,
			})
			s, err := c.FindServerByIP(context.Background(), tc.ip)
			if tc.wantZone != "" {
				if err != nil {
					t.Fatal(err)
				}
				if s.Zone != tc.wantZone || s.ID != "8e3a1111" {
					t.Fatalf("found %s in %q, want 8e3a1111 in %q", s.ID, s.Zone, tc.wantZone)
				}
				return
			}
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			var apiErr *APIError
			gotAPI := 0
			if errors.As(err, &apiErr) {
				gotAPI = apiErr.Status
			}
			if gotAPI != tc.wantAPI {
				t.Fatalf("APIError status = %d, want %d (err %v)", gotAPI, tc.wantAPI, err)
			}
		})
	}
}

func TestPortByIP(t *testing.T) {
	c, _ := replay(t, "k", map[string]reply{"GET /ecc/v1/regions/ir-thr-ba1/ports": {body: samplePorts}})
	p, err := c.PortByIP(context.Background(), "ir-thr-ba1", "188.213.196.86")
	if err != nil {
		t.Fatal(err)
	}
	want := Port{
		ID: "7dee-p", NetworkID: "4cdd-net", MAC: "fa:16:3e:41:45:71", InstanceID: "8e3a1111", Status: "ACTIVE",
		IPs: []string{"188.213.196.86"}, SecurityGroupIDs: []string{"1d374ba1-sg"},
	}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("PortByIP = %+v, want %+v", p, want)
	}
	if _, err := c.PortByIP(context.Background(), "ir-thr-ba1", "9.9.9.9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing IP: err = %v, want ErrNotFound", err)
	}
}

func TestAddPublicIP(t *testing.T) {
	const path = "POST /ecc/v1/regions/ir-thr-ba1/servers/8e3a1111/add-public-ip"
	cases := []struct {
		name     string
		sgIDs    []string
		reply    string
		wantBody string
		want     Port
		wantErr  bool
	}{
		{
			name: "the measured reply", sgIDs: []string{"1d374ba1-sg"}, reply: sampleAddIP,
			wantBody: `{"type":"ipv4","security_groups":["1d374ba1-sg"]}`,
			want:     Port{ID: "7dee-p", MAC: "fa:16:3e:41:45:71", IPs: []string{"188.213.196.86"}, InstanceID: "8e3a1111"},
		},
		{
			name: "no security groups is an empty list, not null", reply: sampleAddIP,
			wantBody: `{"type":"ipv4","security_groups":[]}`,
			want:     Port{ID: "7dee-p", MAC: "fa:16:3e:41:45:71", IPs: []string{"188.213.196.86"}, InstanceID: "8e3a1111"},
		},
		{
			name: "a port without an address still hands back the port", sgIDs: []string{"1d374ba1-sg"},
			reply:    `{"data":{"fixed_ips":[],"port_id":"7dee-p","mac_addr":"fa:16:3e:41:45:71"}}`,
			wantBody: `{"type":"ipv4","security_groups":["1d374ba1-sg"]}`,
			want:     Port{ID: "7dee-p", MAC: "fa:16:3e:41:45:71", InstanceID: "8e3a1111"},
			wantErr:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rs := replay(t, "k", map[string]reply{path: {status: http.StatusCreated, body: tc.reply}})
			p, err := c.AddPublicIP(context.Background(), "ir-thr-ba1", "8e3a1111", tc.sgIDs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(p, tc.want) {
				t.Fatalf("port = %+v, want %+v", p, tc.want)
			}
			req := rs.requests()[0]
			if string(req.body) != tc.wantBody || req.ctype != "application/json" {
				t.Fatalf("body %s (%s), want %s (application/json)", req.body, req.ctype, tc.wantBody)
			}
		})
	}
}

func TestDeletePort(t *testing.T) {
	c, rs := replay(t, "k", map[string]reply{"DELETE /ecc/v1/regions/ir-thr-ba1/ports/7dee-p": {status: http.StatusNoContent}})
	if err := c.DeletePort(context.Background(), "ir-thr-ba1", "7dee-p"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeletePort(context.Background(), "ir-thr-ba1", ""); err == nil || err.Error() != "arvan: no port id" {
		t.Fatalf("empty port id: err = %v, want arvan: no port id", err)
	}
	if n := len(rs.requests()); n != 1 {
		t.Fatalf("%d requests reached Arvan, want 1: an empty port id must not become DELETE .../ports/", n)
	}
}

func TestDomainsFollowsPages(t *testing.T) {
	c, rs := replay(t, "k", map[string]reply{
		"GET /cdn/4.0/domains?page=1&per_page=100": {body: `{"data":[{"domain":"aliwoodbaf.ir"},{"domain":"b.ir"}],"meta":{"current_page":1,"last_page":2}}`},
		"GET /cdn/4.0/domains?page=2&per_page=100": {body: `{"data":[{"domain":"c.ir"}],"meta":{"current_page":2,"last_page":2}}`},
	})
	got, err := c.Domains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"aliwoodbaf.ir", "b.ir", "c.ir"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Domains = %v, want %v", got, want)
	}
	if n := len(rs.requests()); n != 2 {
		t.Fatalf("%d requests, want 2: the walk must stop at last_page", n)
	}
}

func TestARecordsAndSetARecordIP(t *testing.T) {
	const list = "GET /cdn/4.0/domains/aliwoodbaf.ir/dns-records?page=%d&per_page=100&type=a"
	const put = "PUT /cdn/4.0/domains/aliwoodbaf.ir/dns-records/38a9-rec"
	c, rs := replay(t, "k", map[string]reply{
		strings.Replace(list, "%d", "1", 1): {body: `{"data":[` + sampleCNAME + `],"meta":{"current_page":1,"last_page":2}}`},
		strings.Replace(list, "%d", "2", 1): {body: `{"data":[` + sampleRecord + `],"meta":{"current_page":2,"last_page":2}}`},
		put:                                 {body: `{"data":{},"message":"DNS record updated"}`},
	})
	recs, err := c.ARecords(context.Background(), "aliwoodbaf.ir")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("ARecords = %+v, want only the A record", recs)
	}
	rec := recs[0]
	if rec.ID != "38a9-rec" || rec.Name != "bemanche" || rec.TTL != 120 || rec.Cloud || !reflect.DeepEqual(rec.IPs, []string{"37.152.191.4"}) {
		t.Fatalf("record = %+v", rec)
	}

	if err := c.SetARecordIP(context.Background(), "aliwoodbaf.ir", rec, "188.213.196.86"); err != nil {
		t.Fatal(err)
	}
	reqs := rs.requests()
	last := reqs[len(reqs)-1]
	if last.method+" "+last.target != put {
		t.Fatalf("last request %s %s, want %s", last.method, last.target, put)
	}
	var body map[string]any
	if err := json.Unmarshal(last.body, &body); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"cloud", "ip_filter_mode", "name", "ttl", "type", "upstream_https", "value"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("PUT fields %v, want %v (read-only fields must be dropped)", keys, want)
	}
	var want map[string]any
	_ = json.Unmarshal([]byte(`{"type":"a","name":"bemanche","ttl":120,"cloud":false,"upstream_https":"http",
		"ip_filter_mode":{"count":"single","order":"none","geo_filter":"none"},
		"value":[{"ip":"188.213.196.86","port":80,"weight":100,"country":""}]}`), &want)
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("PUT body %s\nwant %v", last.body, want)
	}
}

func TestAPIErrors(t *testing.T) {
	cases := []struct {
		name     string
		rep      reply
		want     string
		notFound bool
	}{
		{"credit short", reply{http.StatusPaymentRequired, `{"message":"Your credit is not enough"}`}, "arvan: 402: Your credit is not enough", false},
		{"read-only key", reply{http.StatusForbidden, `{"message":"You don't have enough permissions for this action"}`}, "arvan: 403: You don't have enough permissions for this action", false},
		{"no JSON body", reply{http.StatusBadGateway, `<html>bad gateway</html>`}, "arvan: 502: Bad Gateway", false},
		{"gone", reply{http.StatusNotFound, `{"message":"Server not found"}`}, "arvan: 404: Server not found", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := replay(t, "k", map[string]reply{"GET /ecc/v1/regions/ir-thr-ba1/servers/8e3a1111": tc.rep})
			_, err := c.Server(context.Background(), "ir-thr-ba1", "8e3a1111")
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.rep.status {
				t.Fatalf("err = %v, want an APIError %d", err, tc.rep.status)
			}
			if err.Error() != tc.want {
				t.Fatalf("Error() = %q, want %q", err.Error(), tc.want)
			}
			if errors.Is(err, ErrNotFound) != tc.notFound {
				t.Fatalf("errors.Is(ErrNotFound) = %v, want %v", !tc.notFound, tc.notFound)
			}
		})
	}
}

// The scripts reach the front inside RunScript's <<'XUIEOF' heredoc, and the
// installer nests the routing script in its own <<'XUIROUTE'.
func TestFrontScriptsParse(t *testing.T) {
	scripts := map[string]string{"routing": routingScript, "install": InstallScript(), "set-primary": SetPrimaryScript()}
	for name, body := range scripts {
		t.Run(name, func(t *testing.T) {
			routeEnds := 0
			for i, line := range strings.Split(body, "\n") {
				if line == "XUIEOF" {
					t.Fatalf("line %d is XUIEOF and would end RunScript's heredoc early", i+1)
				}
				if line == "XUIROUTE" {
					routeEnds++
				}
			}
			if wantEnds := map[string]int{"install": 1}[name]; routeEnds != wantEnds {
				t.Fatalf("%d lines are XUIROUTE, want %d", routeEnds, wantEnds)
			}
			sh, err := exec.LookPath("sh")
			if err != nil {
				t.Skip("sh is not installed")
			}
			path := filepath.Join(t.TempDir(), name+".sh")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(sh, "-n", filepath.ToSlash(path)).CombinedOutput(); err != nil {
				t.Fatalf("sh -n: %v\n%s", err, out)
			}
		})
	}
}

// fakeIP logs every call and answers from out/<args>; fail/<args> makes that
// call exit non-zero. Args become a file name with spaces as _ and / as ~.
const fakeIP = `#!/bin/sh
printf '%s\n' "$*" >>"$FAKE/log"
key=$(printf '%s' "$*" | tr ' /' '_~')
[ -e "$FAKE/fail/$key" ] && exit 2
[ -e "$FAKE/out/$key" ] && cat "$FAKE/out/$key"
exit 0
`

type frontBox struct {
	ip    map[string]string // ip arguments -> stdout
	fail  []string          // ip arguments that fail
	files map[string]string // path under the fake root -> content
}

func twoNICBox(primary string) frontBox {
	return frontBox{
		ip: map[string]string{
			"-4 -o addr show dev eth0 scope global":     "2: eth0    inet 37.152.191.4/24 brd 37.152.191.255 scope global dynamic eth0",
			"-4 -o addr show dev eth1 scope global":     "3: eth1    inet 188.213.196.86/24 brd 188.213.196.255 scope global dynamic eth1",
			"-4 route show default table main dev eth0": "default via 37.152.191.1 proto dhcp src 37.152.191.4 metric 100",
			"-4 route show default table main dev eth1": "default via 188.213.196.1 proto dhcp src 188.213.196.86 metric 100",
			"-4 route show default table main": "default via 37.152.191.1 dev eth0 proto dhcp src 37.152.191.4 metric 100\n" +
				"default via 188.213.196.1 dev eth1 proto dhcp src 188.213.196.86 metric 100",
			"-4 rule show": "0:\tfrom all lookup local\n32766:\tfrom all lookup main\n32767:\tfrom all lookup default",
		},
		files: map[string]string{
			"sys/class/net/eth0/ifindex": "2\n",
			"sys/class/net/eth1/ifindex": "3\n",
			"sys/class/net/lo/ifindex":   "1\n",
			"etc/x-ui-multi-ip/primary":  primary + "\n",
		},
	}
}

// runRouting runs the routing script against a fake ip(8) and a fake /sys, and
// returns the ip calls it made in order.
func runRouting(t *testing.T, box frontBox) ([]string, string, error) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is not installed")
	}
	dir := t.TempDir()
	root, fake, bin := filepath.Join(dir, "root"), filepath.Join(dir, "fake"), filepath.Join(dir, "bin")
	key := strings.NewReplacer(" ", "_", "/", "~").Replace
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	for args, out := range box.ip {
		write(filepath.Join(fake, "out", key(args)), out+"\n", 0o644)
	}
	for _, args := range box.fail {
		write(filepath.Join(fake, "fail", key(args)), "", 0o644)
	}
	for rel, content := range box.files {
		write(filepath.Join(root, filepath.FromSlash(rel)), content, 0o644)
	}
	write(filepath.Join(fake, "log"), "", 0o644)
	write(filepath.Join(bin, "ip"), fakeIP, 0o755)
	script := filepath.Join(dir, "x-ui-multi-ip.sh")
	write(script, routingScript, 0o755)

	cmd := exec.Command(sh, filepath.ToSlash(script))
	cmd.Env = append(os.Environ(),
		"XUI_MULTI_IP_ROOT="+filepath.ToSlash(root),
		"XUI_MULTI_IP_LOCKED=1",
		"FAKE="+filepath.ToSlash(fake),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	out, runErr := cmd.CombinedOutput()
	raw, err := os.ReadFile(filepath.Join(fake, "log"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n"), string(out), runErr
}

func indexOf(calls []string, call string) int {
	for i, c := range calls {
		if c == call {
			return i
		}
	}
	return -1
}

func TestRoutingMovesTheDefaultSafely(t *testing.T) {
	const (
		setEth1 = "route replace default via 188.213.196.1 dev eth1 src 188.213.196.86 metric 50"
		setEth0 = "route replace default via 37.152.191.1 dev eth0 src 37.152.191.4 metric 50"
		delEth0 = "route del default via 37.152.191.1 metric 100 dev eth0 table main"
		delEth1 = "route del default via 188.213.196.1 metric 100 dev eth1 table main"
	)
	common := []string{
		"route replace 37.152.191.0/24 dev eth0 src 37.152.191.4 scope link table 100",
		"route replace default via 37.152.191.1 dev eth0 table 100",
		"route replace 188.213.196.0/24 dev eth1 src 188.213.196.86 scope link table 101",
		"route replace default via 188.213.196.1 dev eth1 table 101",
		"-4 rule add priority 999 lookup main suppress_prefixlength 0",
		"-4 rule add priority 1000 from 37.152.191.4 lookup 100",
		"-4 rule add priority 1001 from 188.213.196.86 lookup 101",
	}
	cases := []struct {
		name      string
		primary   string
		fail      []string
		set       string
		deleted   string
		kept      string
		wantExit1 bool
	}{
		{name: "a reserve becomes active", primary: "188.213.196.86", set: setEth1, deleted: delEth0, kept: delEth1},
		{name: "a primary no longer on the box falls back to eth0", primary: "5.5.5.5", set: setEth0, deleted: delEth1, kept: delEth0},
		{name: "a default that cannot be set deletes nothing", primary: "188.213.196.86", fail: []string{setEth1}, set: setEth1, wantExit1: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			box := twoNICBox(tc.primary)
			box.fail = tc.fail
			calls, out, err := runRouting(t, box)
			for _, want := range common {
				if indexOf(calls, want) < 0 {
					t.Errorf("missing %q\ncalls:\n%s\noutput:\n%s", want, strings.Join(calls, "\n"), out)
				}
			}
			set := indexOf(calls, tc.set)
			if set < 0 {
				t.Fatalf("missing %q\ncalls:\n%s", tc.set, strings.Join(calls, "\n"))
			}
			if tc.wantExit1 {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
					t.Fatalf("exit = %v, want 1\n%s", err, out)
				}
				for _, c := range calls {
					if strings.HasPrefix(c, "route del") {
						t.Fatalf("deleted %q although the new default never stood", c)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("exit = %v\n%s", err, out)
			}
			if del := indexOf(calls, tc.deleted); del < set {
				t.Fatalf("%q at %d, want after %q at %d", tc.deleted, del, tc.set, set)
			}
			if indexOf(calls, tc.kept) >= 0 {
				t.Fatalf("deleted the active NIC's own default: %q", tc.kept)
			}
		})
	}
}

func TestRoutingFindsEachNICsGateway(t *testing.T) {
	cases := []struct {
		name   string
		addr   string
		lease  string
		table  string
		wantGW string
	}{
		{name: "from the DHCP lease", addr: "188.213.196.86/24", lease: "ADDRESS=188.213.196.86\nROUTER=188.213.196.254\n", wantGW: "188.213.196.254"},
		{name: "from its own table", addr: "188.213.196.86/24", table: "default via 188.213.196.253 dev eth1", wantGW: "188.213.196.253"},
		{name: "the first host of a /23", addr: "188.213.197.86/23", wantGW: "188.213.196.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			box := twoNICBox("37.152.191.4")
			delete(box.ip, "-4 route show default table main dev eth1")
			box.ip["-4 route show default table main"] = "default via 37.152.191.1 dev eth0 proto dhcp src 37.152.191.4 metric 100"
			box.ip["-4 -o addr show dev eth1 scope global"] = "3: eth1    inet " + tc.addr + " scope global eth1"
			if tc.lease != "" {
				box.files["run/systemd/netif/leases/3"] = tc.lease
			}
			if tc.table != "" {
				box.ip["-4 route show default table 101"] = tc.table
			}
			calls, out, err := runRouting(t, box)
			if err != nil {
				t.Fatalf("exit = %v\n%s", err, out)
			}
			if want := "route replace default via " + tc.wantGW + " dev eth1 table 101"; indexOf(calls, want) < 0 {
				t.Fatalf("missing %q\ncalls:\n%s", want, strings.Join(calls, "\n"))
			}
		})
	}
}

func TestRoutingReconcilesRules(t *testing.T) {
	box := twoNICBox("37.152.191.4")
	box.ip["-4 rule show"] = "0:\tfrom all lookup local\n" +
		"999:\tfrom all lookup main suppress_prefixlength 0\n" +
		"1000:\tfrom 37.152.191.4 lookup 100\n" +
		"1002:\tfrom 5.5.5.5 lookup 102\n" +
		"32766:\tfrom all lookup main"
	calls, out, err := runRouting(t, box)
	if err != nil {
		t.Fatalf("exit = %v\n%s", err, out)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "-4 rule add priority 999 ") || strings.HasPrefix(c, "-4 rule add priority 1000 ") {
			t.Errorf("re-added a rule that was already there: %q", c)
		}
		if strings.HasPrefix(c, "-4 rule del") && c != "-4 rule del priority 1002 from 5.5.5.5 lookup 102" {
			t.Errorf("deleted a rule that should stay: %q", c)
		}
	}
	for _, want := range []string{"-4 rule add priority 1001 from 188.213.196.86 lookup 101", "-4 rule del priority 1002 from 5.5.5.5 lookup 102"} {
		if indexOf(calls, want) < 0 {
			t.Errorf("missing %q\ncalls:\n%s", want, strings.Join(calls, "\n"))
		}
	}
}
