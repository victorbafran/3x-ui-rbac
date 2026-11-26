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
	relayOn    map[string]string
	deleted    []string
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
				{"code":"ir-thr-ba1","country":"Iran","city":"Tehran","dc":"Bamdad","create":false,"visible":true}]}`)
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
