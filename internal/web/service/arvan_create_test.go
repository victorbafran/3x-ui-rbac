package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/mhsanaei/3x-ui/v3/internal/arvan"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
)

// fakeArvan is one account with a zone ir-thr-fr1 that makes servers.
type fakeArvan struct {
	mu         sync.Mutex
	createAs   int      // status of the create; 0 makes the server
	statuses   []string // what each read of the new server says, the last repeating
	reads      int
	ip         string
	keys       []string
	creates    []map[string]any
	madeByName bool // the create's reply was lost but the server exists
	deleted    []string
	records    []string
}

func (f *fakeArvan) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		write := func(s string) { _, _ = io.WriteString(w, s) }
		route := r.Method + " " + r.URL.Path
		switch {
		case route == "GET /ecc/v1/regions":
			write(`{"data":[{"code":"ir-thr-fr1","country":"Iran","city":"Tehran","dc":"Foroogh","create":true,"visible":true},
				{"code":"ir-thr-ba1","country":"Iran","city":"Tehran","dc":"Bamdad","create":false,"visible":true},
				{"code":"eu-west1-a","country":"Germany","city":"Karlsruhe","dc":"Goethe","create":true,"visible":false}]}`)
		case route == "GET /ecc/v1/regions/eu-west1-a/servers/de1":
			write(`{"data":{"id":"de1","name":"de","status":"ACTIVE","addresses":{"public":[{"addr":"185.226.1.9","version":"4","is_public":true}]}}}`)
		case route == "GET /cdn/4.0/domains":
			write(`{"data":[{"domain":"aliwoodbaf.ir"},{"domain":"video-epic.ir"}],"meta":{"current_page":1,"last_page":1}}`)
		case route == "GET /cdn/4.0/domains/aliwoodbaf.ir/dns-records":
			write(`{"data":[{"id":"r-milichi","type":"a","name":"milichi","value":[{"ip":"188.213.199.99","port":null,"weight":100,"country":""}],"ttl":120,"cloud":false},
				{"id":"r-bem","type":"a","name":"bemanche","value":[{"ip":"212.95.35.41","port":80,"weight":100,"country":""}],"ttl":120,"cloud":false}],"meta":{"current_page":1,"last_page":1}}`)
		case route == "POST /cdn/4.0/domains/aliwoodbaf.ir/dns-records":
			var rec map[string]any
			_ = json.Unmarshal(body, &rec)
			f.records = append(f.records, "new "+rec["name"].(string)+" "+rec["value"].([]any)[0].(map[string]any)["ip"].(string))
			write(`{"data":{"id":"r-new"}}`)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/cdn/4.0/domains/aliwoodbaf.ir/dns-records/"):
			var rec map[string]any
			_ = json.Unmarshal(body, &rec)
			f.records = append(f.records, "moved "+strings.TrimPrefix(r.URL.Path, "/cdn/4.0/domains/aliwoodbaf.ir/dns-records/")+" "+rec["value"].([]any)[0].(map[string]any)["ip"].(string))
			write(`{"data":{}}`)
		case route == "GET /ecc/v1/regions/ir-thr-fr1/sizes":
			write(`{"data":[{"id":"g1-12-4-0","disk":75,"memory":12,"cpu_count":4,"price_per_hour":68700},
				{"id":"eco-1-1-0","disk":25,"memory":1,"cpu_count":1,"price_per_hour":13860,"price_per_month":9977400}]}`)
		case route == "GET /ecc/v1/regions/ir-thr-fr1/images":
			write(`{"data":[{"name":"ubuntu","images":[{"id":"u22","name":"22.04","distribution_name":"ubuntu","ssh_key":true},{"id":"u24","name":"24.04","distribution_name":"ubuntu","ssh_key":true}]},
				{"name":"windows","images":[{"id":"w25","name":"2025","distribution_name":"windows","ssh_key":false}]}]}`)
		case route == "GET /ecc/v1/regions/ir-thr-fr1/servers/options":
			write(`{"data":{"droplet_count":0,"droplet_limit":5,"network_id":"net-1"}}`)
		case route == "GET /ecc/v1/regions/ir-thr-fr1/securities":
			write(`{"data":[{"id":"sg-def","real_name":"centralized_arDefault","default":true}]}`)
		case route == "POST /ecc/v1/regions/ir-thr-fr1/ssh-keys":
			var k map[string]string
			_ = json.Unmarshal(body, &k)
			f.keys = append(f.keys, k["public_key"])
			write(`{"data":{}}`)
		case route == "POST /ecc/v1/regions/ir-thr-fr1/servers":
			var c map[string]any
			_ = json.Unmarshal(body, &c)
			f.creates = append(f.creates, c)
			if f.createAs != 0 {
				w.WriteHeader(f.createAs)
				write(`{"message":"refused"}`)
				return
			}
			write(`{"data":{"id":"srv-new","status":"build"}}`)
		case route == "GET /ecc/v1/regions/ir-thr-fr1/servers":
			if f.madeByName && len(f.creates) > 0 {
				write(`{"data":[{"id":"srv-new","name":"` + f.creates[0]["name"].(string) + `","status":"build"}]}`)
				return
			}
			write(`{"data":[{"id":"old","name":"other","status":"ACTIVE","addresses":{"public":[{"addr":"37.152.187.164","version":"4","is_public":true}]}}]}`)
		case route == "GET /ecc/v1/regions/ir-thr-ba1/servers":
			w.WriteHeader(http.StatusForbidden)
			write(`{"message":"You don't have enough permissions"}`)
		case route == "GET /ecc/v1/regions/ir-thr-fr1/servers/old":
			write(`{"data":{"id":"old","name":"other","status":"ACTIVE","addresses":{"public":[{"addr":"37.152.187.164","version":"4","is_public":true}]}}}`)
		case route == "GET /ecc/v1/regions/ir-thr-fr1/servers/free":
			write(`{"data":{"id":"free","name":"spare","status":"ACTIVE","addresses":{"public":[{"addr":"37.152.190.77","version":"4","is_public":true}]}}}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/ecc/v1/regions/ir-thr-fr1/servers/"):
			f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/ecc/v1/regions/ir-thr-fr1/servers/")+"?"+r.URL.RawQuery)
			w.WriteHeader(http.StatusNoContent)
		case route == "GET /ecc/v1/regions/ir-thr-fr1/servers/srv-new":
			st := f.statuses[min(f.reads, len(f.statuses)-1)]
			f.reads++
			addr := ""
			if st == "ACTIVE" {
				addr = `"public":[{"addr":"` + f.ip + `","version":"4","is_public":true}]`
			}
			write(`{"data":{"id":"srv-new","status":"` + st + `","addresses":{` + addr + `}}}`)
		default:
			t.Errorf("unexpected %s", route)
			w.WriteHeader(http.StatusTeapot)
		}
	})
}

type createRun struct {
	fake    *fakeArvan
	added   []RelayRequest
	bound   []int
	sshOK   bool
	enrolls []string // statuses the added relay goes through
}

func setupArvanCreate(t *testing.T, fake *fakeArvan) *createRun {
	t.Helper()
	setupRelayDB(t)
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	oldBase := arvan.Base
	arvan.Base = srv.URL
	t.Cleanup(func() { arvan.Base = oldBase })
	sealed, err := sealArvanKey("apikey test-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := (&SettingService{}).SetArvanApiKey(sealed); err != nil {
		t.Fatal(err)
	}
	run := &createRun{fake: fake, sshOK: true, enrolls: []string{RelayWorking, RelayReady}}
	saved := []any{arvanBootWait, arvanSSHWait, arvanEnrollWait, arvanCreatePoll, arvanSSHReady, arvanAddFront, arvanBindFront, arvanFrontStatus}
	t.Cleanup(func() {
		arvanBootWait, arvanSSHWait, arvanEnrollWait, arvanCreatePoll = saved[0].(time.Duration), saved[1].(time.Duration), saved[2].(time.Duration), saved[3].(time.Duration)
		arvanSSHReady = saved[4].(func(context.Context, relay.Credentials) error)
		arvanAddFront = saved[5].(func(*ArvanService, RelayRequest) (int, error))
		arvanBindFront = saved[6].(func(*ArvanService, int) error)
		arvanFrontStatus = saved[7].(func(int) (string, string))
		setArvanCreate(func(st *ArvanCreateState) { *st = ArvanCreateState{} })
	})
	arvanBootWait, arvanSSHWait, arvanEnrollWait, arvanCreatePoll = time.Second, 300*time.Millisecond, time.Second, 5*time.Millisecond
	arvanSSHReady = func(_ context.Context, c relay.Credentials) error {
		if !run.sshOK {
			return errors.New("ssh: handshake failed")
		}
		if _, err := ssh.ParsePrivateKey(c.PrivateKey); err != nil || c.User != "root" || c.Port != 22 {
			return fmt.Errorf("dialled with %+v", c)
		}
		return nil
	}
	arvanAddFront = func(_ *ArvanService, req RelayRequest) (int, error) {
		run.added = append(run.added, req)
		return 41, nil
	}
	reads := 0
	arvanFrontStatus = func(int) (string, string) {
		st := run.enrolls[min(reads, len(run.enrolls)-1)]
		reads++
		return st, "could not reach it"
	}
	arvanBindFront = func(_ *ArvanService, id int) error {
		run.bound = append(run.bound, id)
		return nil
	}
	return run
}

func waitCreateDone(t *testing.T) ArvanCreateState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := (&ArvanService{}).CreateState()
		if !st.Running {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("the make-and-add never finished: %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var createReq = ArvanCreateRequest{Zone: "ir-thr-fr1", Size: "eco-1-1-0", Image: "u24", Name: "ir2"}

// The catalog offers the zones that take servers, the zone's plans cheapest first and only the
// Linux images that take a key, Ubuntu 24.04 picked.
func TestTheCatalogOffersWhatAFrontCanBeMadeOn(t *testing.T) {
	setupArvanCreate(t, &fakeArvan{})
	cat, err := (&ArvanService{}).Catalog("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Zones) != 1 || cat.Zone != "ir-thr-fr1" || cat.ServerLimit != 5 {
		t.Fatalf("zones %+v, zone %q, limit %d", cat.Zones, cat.Zone, cat.ServerLimit)
	}
	if len(cat.Sizes) != 2 || cat.Sizes[0].ID != "eco-1-1-0" {
		t.Fatalf("sizes %+v; want the cheapest first", cat.Sizes)
	}
	if len(cat.Images) != 2 || cat.DefaultImage != "u24" {
		t.Fatalf("images %+v, default %q; want Ubuntu only, 24.04 picked", cat.Images, cat.DefaultImage)
	}
}

// The panel makes the server with a key it generated, waits for it to boot and answer SSH on
// that key, adds it as a BackPack entry server with the same key, so no password, and binds
// it to Arvan once it is set up.
func TestAFrontIsMadeInArvanAndAddedWithNoPassword(t *testing.T) {
	fake := &fakeArvan{statuses: []string{"BUILD", "BUILD", "ACTIVE"}, ip: "37.152.190.9"}
	run := setupArvanCreate(t, fake)
	if err := (&ArvanService{}).StartCreate(createReq); err != nil {
		t.Fatal(err)
	}
	st := waitCreateDone(t)
	if st.Error != "" || st.Step != arvanStepDone || st.IP != "37.152.190.9" || st.ServerID != "srv-new" || st.RelayID != 41 {
		t.Fatalf("state %+v", st)
	}
	if len(fake.creates) != 1 {
		t.Fatalf("%d creates", len(fake.creates))
	}
	c := fake.creates[0]
	if c["name"] != "ir2" || c["flavor_id"] != "eco-1-1-0" || c["image_id"] != "u24" || c["ssh_key"] != true || c["count"] != float64(1) {
		t.Fatalf("create body %v", c)
	}
	if groups, _ := json.Marshal(c["security_groups"]); string(groups) != `[{"name":"sg-def"}]` {
		t.Fatalf("security groups %s", groups)
	}
	if len(run.added) != 1 {
		t.Fatalf("added %d entry servers", len(run.added))
	}
	a := run.added[0]
	if a.Address != "37.152.190.9" || a.Role != string(model.RelayFront) || a.Tool != "backpack" || a.Transport != "stealth" || a.Password != "" {
		t.Fatalf("added as %+v", a)
	}
	signer, err := ssh.ParsePrivateKey([]byte(a.PrivateKey))
	if err != nil {
		t.Fatalf("the add carries no usable key: %v", err)
	}
	if line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))); len(fake.keys) != 1 || !strings.HasPrefix(fake.keys[0], line) {
		t.Fatalf("the key given to Arvan is not the one enrolled with: %v", fake.keys)
	}
	if len(run.bound) != 1 || run.bound[0] != 41 {
		t.Fatalf("bound %v", run.bound)
	}
}

// Whatever goes wrong before the server answers on the panel's key, nothing is enrolled; a
// server that was made is named, since Arvan bills it until it is deleted.
func TestAFailedMakeEnrollsNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fake  *fakeArvan
		ssh   bool
		error string
		// a server Arvan reports in error is not waited out to the timeout
		quick bool
	}{
		{"Arvan refuses the create", &fakeArvan{createAs: 402, statuses: []string{"BUILD"}}, true, "did not make the server", false},
		{"the server lands in error", &fakeArvan{statuses: []string{"BUILD", "ERROR"}}, true, "billed until deleted", true},
		{"it never boots", &fakeArvan{statuses: []string{"BUILD"}}, true, "still build", false},
		{"it never takes the key", &fakeArvan{statuses: []string{"ACTIVE"}, ip: "37.152.190.9"}, false, "did not take the panel's key", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := setupArvanCreate(t, tc.fake)
			run.sshOK = tc.ssh
			if err := (&ArvanService{}).StartCreate(createReq); err != nil {
				t.Fatal(err)
			}
			st := waitCreateDone(t)
			if !strings.Contains(st.Error, tc.error) {
				t.Fatalf("error %q; want %q", st.Error, tc.error)
			}
			if tc.quick && strings.Contains(st.Error, "gave up") {
				t.Fatalf("error %q: an error status was waited out", st.Error)
			}
			if len(run.added) != 0 || len(run.bound) != 0 {
				t.Fatalf("enrolled %v, bound %v after a failed make", run.added, run.bound)
			}
		})
	}
}

// A gateway's 5xx may hide a server that was made; it is looked up by name and carried on.
func TestALostCreateReplyFindsTheServerByName(t *testing.T) {
	fake := &fakeArvan{createAs: 504, madeByName: true, statuses: []string{"ACTIVE"}, ip: "37.152.190.9"}
	run := setupArvanCreate(t, fake)
	if err := (&ArvanService{}).StartCreate(createReq); err != nil {
		t.Fatal(err)
	}
	if st := waitCreateDone(t); st.Error != "" || st.ServerID != "srv-new" || len(run.added) != 1 {
		t.Fatalf("state %+v, added %d", st, len(run.added))
	}
}

// An entry server whose setup fails after the add is reported, and nothing is bound to it.
func TestAFrontThatFailsToSetUpIsNotBound(t *testing.T) {
	run := setupArvanCreate(t, &fakeArvan{statuses: []string{"ACTIVE"}, ip: "37.152.190.9"})
	run.enrolls = []string{RelayWorking, RelayError}
	if err := (&ArvanService{}).StartCreate(createReq); err != nil {
		t.Fatal(err)
	}
	st := waitCreateDone(t)
	if !strings.Contains(st.Error, "setting it up failed: could not reach it") || len(run.bound) != 0 {
		t.Fatalf("state %+v, bound %v", st, run.bound)
	}
}

// What the zone does not offer is refused before anything is made or registered.
func TestABadRequestMakesNothing(t *testing.T) {
	fake := &fakeArvan{}
	setupArvanCreate(t, fake)
	seedRelays(t, model.Relay{Name: "taken", Address: "203.0.113.4", Role: model.RelayExit})
	for _, tc := range []struct {
		req  ArvanCreateRequest
		want string
	}{
		{ArvanCreateRequest{Zone: "ir-thr-fr1", Size: "nope", Image: "u24", Name: "ir2"}, "no size"},
		{ArvanCreateRequest{Zone: "ir-thr-fr1", Size: "eco-1-1-0", Image: "w25", Name: "ir2"}, "no Linux image"},
		{ArvanCreateRequest{Zone: "ir-thr-ba1", Size: "eco-1-1-0", Image: "u24", Name: "ir2"}, "does not take new servers"},
		{ArvanCreateRequest{Zone: "ir-thr-fr1", Size: "eco-1-1-0", Image: "u24", Name: "taken"}, "already named"},
		{ArvanCreateRequest{Zone: "ir-thr-fr1", Size: "eco-1-1-0", Image: "u24", Name: "a b"}, "space"},
	} {
		if err := (&ArvanService{}).StartCreate(tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v; want %q", tc.req, err, tc.want)
		}
	}
	if len(fake.creates) != 0 || len(fake.keys) != 0 {
		t.Fatalf("a refused request still made %d servers and %d keys", len(fake.creates), len(fake.keys))
	}

	setArvanCreate(func(st *ArvanCreateState) { st.Running = true })
	if err := (&ArvanService{}).StartCreate(createReq); err == nil || !strings.Contains(err.Error(), "already being made") {
		t.Fatalf("a second make while one runs: %v", err)
	}
}

// The account's servers are listed zone by zone, a zone the key may not read skipped, each
// with the relay of this panel at its address.
func TestTheAccountsServersAreListedWithTheirRelay(t *testing.T) {
	setupArvanCreate(t, &fakeArvan{})
	seedRelays(t, model.Relay{Name: "Ir", Address: "37.152.187.164", Role: model.RelayFront})
	got, err := (&ArvanService{}).Servers()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Zone != "ir-thr-fr1" || got[0].Status != "active" || got[0].Relay != "Ir" || len(got[0].IPs) != 1 {
		t.Fatalf("servers %+v", got)
	}
}

// A server is deleted from the account, but not one whose IP is a relay's address here, nor
// the one being made.
func TestAServerInUseHereIsNotDeleted(t *testing.T) {
	fake := &fakeArvan{}
	setupArvanCreate(t, fake)
	seedRelays(t, model.Relay{Name: "Ir", Address: "37.152.187.164", Role: model.RelayFront})
	svc := &ArvanService{}
	if err := svc.DeleteServer("ir-thr-fr1", "old"); err == nil || !strings.Contains(err.Error(), "remove that server here first") {
		t.Fatalf("deleting a server in use: %v", err)
	}
	setArvanCreate(func(st *ArvanCreateState) { st.Running, st.ServerID = true, "free" })
	if err := svc.DeleteServer("ir-thr-fr1", "free"); err == nil || !strings.Contains(err.Error(), "still being made") {
		t.Fatalf("deleting the server being made: %v", err)
	}
	if len(fake.deleted) != 0 {
		t.Fatalf("deleted %v", fake.deleted)
	}
	setArvanCreate(func(st *ArvanCreateState) { *st = ArvanCreateState{} })
	if err := svc.DeleteServer("ir-thr-fr1", "free"); err != nil {
		t.Fatal(err)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "free?forceDelete=true" {
		t.Fatalf("deleted %v", fake.deleted)
	}
}

// The purpose is checked before anything is made: an exit only outside Iran, a transport the
// tool knows, a domain on the account and a record name Arvan takes.
func TestThePurposeIsCheckedBeforeAnythingIsMade(t *testing.T) {
	fake := &fakeArvan{}
	setupArvanCreate(t, fake)
	svc := &ArvanService{}
	c, err := svc.client()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		p       ArvanPurpose
		country string
		want    string
	}{
		{ArvanPurpose{Role: "exit"}, "Iran", "out through Iran"},
		{ArvanPurpose{Role: "relay"}, "Iran", `not "relay"`},
		{ArvanPurpose{Tool: "backpack", Transport: "udpovertcp"}, "Iran", "not a transport"},
		{ArvanPurpose{Tool: "backhaul", Transport: "udp"}, "Iran", "udp carries only UDP"},
		{ArvanPurpose{Domain: "other.ir", Record: "ir2"}, "Iran", "not on this Arvan account"},
		{ArvanPurpose{Domain: "aliwoodbaf.ir", Record: "ir 2"}, "Iran", "not a record name"},
		{ArvanPurpose{Domain: "aliwoodbaf.ir", Record: "-ir"}, "Iran", "not a record name"},
	} {
		if _, err := svc.normalizePurpose(c, tc.p, tc.country); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v in %s: %v; want %q", tc.p, tc.country, err, tc.want)
		}
	}
	got, err := svc.normalizePurpose(c, ArvanPurpose{}, "Iran")
	if err != nil || got != (ArvanPurpose{Role: "front", Tool: "backpack", Transport: "stealth"}) {
		t.Fatalf("the default purpose: %+v, %v", got, err)
	}
	got, err = svc.normalizePurpose(c, ArvanPurpose{Role: "front", Tool: "backhaul", Domain: "AliWoodBaf.ir", Record: "Ir2"}, "Iran")
	if err != nil || got != (ArvanPurpose{Role: "front", Tool: "backhaul", Transport: "tcp", Domain: "aliwoodbaf.ir", Record: "ir2"}) {
		t.Fatalf("Backhaul behind a record: %+v, %v", got, err)
	}
	got, err = svc.normalizePurpose(c, ArvanPurpose{Role: "exit", Tool: "backpack", Domain: "aliwoodbaf.ir", Record: "x"}, "Germany")
	if err != nil || got != (ArvanPurpose{Role: "exit"}) {
		t.Fatalf("an exit in Germany: %+v, %v", got, err)
	}
	if len(fake.creates)+len(fake.records) != 0 {
		t.Fatal("checking a purpose wrote something")
	}
}

// A made entry server behind a new record: the record is made, becomes the server's public
// name, the run went through dns before bind, and the tool and transport chosen are the ones added.
func TestAMadeFrontGoesBehindItsRecord(t *testing.T) {
	fake := &fakeArvan{statuses: []string{"ACTIVE"}, ip: "37.152.190.9"}
	run := setupArvanCreate(t, fake)
	req := createReq
	req.ArvanPurpose = ArvanPurpose{Tool: "backhaul", Transport: "wss", Domain: "aliwoodbaf.ir", Record: "ir2"}
	var id int
	arvanAddFront = func(_ *ArvanService, r RelayRequest) (int, error) {
		run.added = append(run.added, r)
		row := model.Relay{Name: r.Name, Address: r.Address, Role: model.RelayFront}
		seedRelays(t, row)
		var got model.Relay
		if err := database.GetDB().Where("name = ?", r.Name).First(&got).Error; err != nil {
			t.Fatal(err)
		}
		id = got.Id
		return got.Id, nil
	}
	if err := (&ArvanService{}).StartCreate(req); err != nil {
		t.Fatal(err)
	}
	st := waitCreateDone(t)
	if st.Error != "" {
		t.Fatalf("state %+v", st)
	}
	if want := "key,server,boot,ssh,add,setup,dns,bind"; strings.Join(st.Steps, ",") != want {
		t.Fatalf("steps %v; want %s", st.Steps, want)
	}
	if a := run.added[0]; a.Tool != "backhaul" || a.Transport != "wss" || a.Role != "front" {
		t.Fatalf("added as %+v", a)
	}
	if len(fake.records) != 1 || fake.records[0] != "new ir2 37.152.190.9" {
		t.Fatalf("records %v", fake.records)
	}
	if r := frontRow(t, id); r.PublicName != "ir2.aliwoodbaf.ir" {
		t.Fatalf("public name %q", r.PublicName)
	}
}

// A record already there is moved to the new server when it points at an address no relay
// here has, so configs customers already hold reach it, but never away from a relay here.
func TestARecordIsMovedOnlyOffAnAddressNotInUseHere(t *testing.T) {
	fake := &fakeArvan{}
	setupArvanCreate(t, fake)
	c, err := (&ArvanService{}).client()
	if err != nil {
		t.Fatal(err)
	}
	seedRelays(t, model.Relay{Name: "panel", Address: "212.95.35.41", Role: model.RelayExit})
	if err := arvanPointRecord(c, 99, "aliwoodbaf.ir", "milichi", "37.152.190.9"); err != nil {
		t.Fatal(err)
	}
	if err := arvanPointRecord(c, 99, "aliwoodbaf.ir", "bemanche", "37.152.190.9"); err == nil || !strings.Contains(err.Error(), "the address of panel here") {
		t.Fatalf("moving a record off another relay: %v", err)
	}
	if err := arvanPointRecord(c, 99, "aliwoodbaf.ir", "bemanche", "212.95.35.41"); err != nil {
		t.Fatalf("a record already on the address: %v", err)
	}
	if len(fake.records) != 1 || fake.records[0] != "moved r-milichi 37.152.190.9" {
		t.Fatalf("records %v", fake.records)
	}
}

// A failed DNS step leaves a working entry server, still bound for swaps, and says so.
func TestAFailedRecordStillBindsTheFront(t *testing.T) {
	run := setupArvanCreate(t, &fakeArvan{statuses: []string{"ACTIVE"}, ip: "37.152.190.9"})
	saved := arvanPointRecord
	arvanPointRecord = func(*arvan.Client, int, string, string, string) error { return errors.New("arvan: 403: no permission") }
	t.Cleanup(func() { arvanPointRecord = saved })
	req := createReq
	req.ArvanPurpose = ArvanPurpose{Domain: "aliwoodbaf.ir", Record: "ir2"}
	if err := (&ArvanService{}).StartCreate(req); err != nil {
		t.Fatal(err)
	}
	st := waitCreateDone(t)
	if !strings.Contains(st.Error, "bound for swaps, but ir2.aliwoodbaf.ir could not be pointed at it") || len(run.bound) != 1 {
		t.Fatalf("state %+v, bound %v", st, run.bound)
	}
}

// A server already on the account is added by its root password once, for the purpose
// chosen; one in use here, or with no password, is refused.
func TestAServerAlreadyThereIsAddedWithItsPassword(t *testing.T) {
	fake := &fakeArvan{}
	run := setupArvanCreate(t, fake)
	svc := &ArvanService{}
	adopt := ArvanAdoptRequest{Zone: "ir-thr-fr1", ServerID: "free", Name: "spare", Password: "pw"}
	noPass := adopt
	noPass.Password = ""
	if err := svc.StartAdopt(noPass); err == nil || !strings.Contains(err.Error(), "password is needed") {
		t.Fatalf("no password: %v", err)
	}
	seedRelays(t, model.Relay{Name: "Ir", Address: "37.152.187.164", Role: model.RelayFront})
	inUse := adopt
	inUse.ServerID, inUse.Name = "old", "again"
	if err := svc.StartAdopt(inUse); err == nil || !strings.Contains(err.Error(), "already Ir on this panel") {
		t.Fatalf("a server in use: %v", err)
	}
	if err := svc.StartAdopt(adopt); err != nil {
		t.Fatal(err)
	}
	st := waitCreateDone(t)
	if st.Error != "" || strings.Join(st.Steps, ",") != "add,setup,bind" || st.IP != "37.152.190.77" {
		t.Fatalf("state %+v", st)
	}
	a := run.added
	if len(a) != 1 || a[0].Password != "pw" || a[0].PrivateKey != "" || a[0].Address != "37.152.190.77" || a[0].Tool != "backpack" {
		t.Fatalf("added %+v", a)
	}
	if len(fake.creates) != 0 || len(fake.keys) != 0 {
		t.Fatal("adopting made a server or a key")
	}
}

// An exit outside Iran has no tunnel, record or Arvan binding: it is added and set up, nothing more.
func TestAnExitIsAddedWithoutTunnelRecordOrBinding(t *testing.T) {
	fake := &fakeArvan{}
	run := setupArvanCreate(t, fake)
	err := (&ArvanService{}).StartAdopt(ArvanAdoptRequest{
		Zone: "eu-west1-a", ServerID: "de1", Name: "de", Password: "pw",
		ArvanPurpose: ArvanPurpose{Role: "exit", Tool: "backpack", Domain: "aliwoodbaf.ir", Record: "de"},
	})
	if err != nil {
		t.Fatal(err)
	}
	st := waitCreateDone(t)
	if st.Error != "" || strings.Join(st.Steps, ",") != "add,setup" {
		t.Fatalf("state %+v", st)
	}
	if a := run.added; len(a) != 1 || a[0].Role != "exit" || a[0].Tool != "" || a[0].Transport != "" || a[0].TunnelPort != 0 {
		t.Fatalf("added %+v", a)
	}
	if len(run.bound) != 0 || len(fake.records) != 0 {
		t.Fatalf("an exit was bound %v or given records %v", run.bound, fake.records)
	}
}
