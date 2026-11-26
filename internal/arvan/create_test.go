package arvan

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// The live replies of 2026-11-26, cut down: a size's price may be a number or a string.
const (
	sampleSizes = `{"data":[
		{"id":"eco-1-1-0","name":"eco-small1","disk":25,"memory":1,"cpu_count":1,"type":"economic","price_per_hour":13860,"price_per_month":9977400},
		{"id":"g1-12-4-0","name":"g1","disk":75,"memory":12,"cpu_count":4,"type":"general","price_per_hour":"68700","price_per_month":"49464000"}]}`
	sampleDistros = `{"data":[
		{"name":"ubuntu","images":[{"id":"f1cc-u24","name":"24.04","distribution_name":"ubuntu","ssh_key":true,"ssh_password":true}]},
		{"name":"windows","images":[{"id":"6b39-w25","name":"2025","distribution_name":"windows","ssh_key":false,"ssh_password":false}]}]}`
	sampleOptions = `{"data":{"region_id":1,"droplet_count":0,"droplet_limit":5,"currency":"irr","network_id":"e914-net","ipv6_support":true}}`
	sampleGroups  = `{"data":[
		{"id":"ab4d-cdn","name":"CDN","real_name":"centralized_arCdnGroup","readonly":true,"default":false},
		{"id":"1d37-def","name":"Default","real_name":"centralized_arDefault","readonly":true,"default":true}]}`
)

func TestTheCatalogIsReadAsArvanSendsIt(t *testing.T) {
	c, _ := replay(t, "k", map[string]reply{
		"GET /ecc/v1/regions/ir-thr-fr1/sizes":                     {body: sampleSizes},
		"GET /ecc/v1/regions/ir-thr-fr1/images?type=distributions": {body: sampleDistros},
		"GET /ecc/v1/regions/ir-thr-fr1/servers/options":           {body: sampleOptions},
		"GET /ecc/v1/regions/ir-thr-fr1/securities":                {body: sampleGroups},
	})
	ctx := context.Background()
	sizes, err := c.Sizes(ctx, "ir-thr-fr1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Size{
		{ID: "eco-1-1-0", Name: "eco-small1", CPUs: 1, MemoryGB: 1, DiskGB: 25, PricePerHour: 13860, PricePerMonth: 9977400, Type: "economic"},
		{ID: "g1-12-4-0", Name: "g1", CPUs: 4, MemoryGB: 12, DiskGB: 75, PricePerHour: 68700, PricePerMonth: 49464000, Type: "general"},
	}
	if len(sizes) != 2 || sizes[0] != want[0] || sizes[1] != want[1] {
		t.Fatalf("sizes %+v\nwant %+v", sizes, want)
	}
	imgs, err := c.Distributions(ctx, "ir-thr-fr1")
	if err != nil || len(imgs) != 2 || imgs[0] != (Image{ID: "f1cc-u24", Version: "24.04", Distribution: "ubuntu", SSHKey: true}) || imgs[1].SSHKey {
		t.Fatalf("images %+v, %v", imgs, err)
	}
	opts, err := c.ServerOptions(ctx, "ir-thr-fr1")
	if err != nil || opts != (ServerOptions{NetworkID: "e914-net", DropletCount: 0, DropletLimit: 5}) {
		t.Fatalf("options %+v, %v", opts, err)
	}
	groups, err := c.SecurityGroups(ctx, "ir-thr-fr1")
	if err != nil {
		t.Fatal(err)
	}
	if g, ok := DefaultSecurityGroup(groups); !ok || g.ID != "1d37-def" {
		t.Fatalf("default group %+v, %v", g, ok)
	}
	if g, ok := DefaultSecurityGroup([]SecurityGroup{{ID: "x", RealName: "arCdnGroup"}, {ID: "y", RealName: "arDefault"}}); !ok || g.ID != "y" {
		t.Fatalf("by real name %+v, %v", g, ok)
	}
}

// The create body carries the fields the Terraform provider sends: the group's id under
// "name", ssh_key on with the registered key's name, one server, HA off.
func TestAServerIsCreatedWithTheRegisteredKey(t *testing.T) {
	c, rs := replay(t, "k", map[string]reply{
		"POST /ecc/v1/regions/ir-thr-fr1/ssh-keys": {status: 201, body: `{"data":{"name":"xui-ir"},"message":"ok"}`},
		"POST /ecc/v1/regions/ir-thr-fr1/servers":  {status: 201, body: `{"data":{"id":"d0d4-new","name":"ir","status":"BUILD"},"message":"ok"}`},
	})
	ctx := context.Background()
	if err := c.AddSSHKey(ctx, "ir-thr-fr1", "xui-ir", "ssh-ed25519 AAAA x-ui"); err != nil {
		t.Fatal(err)
	}
	id, err := c.CreateServer(ctx, "ir-thr-fr1", NewServer{
		Name: "ir", SizeID: "eco-1-1-0", ImageID: "f1cc-u24", NetworkID: "e914-net", SecurityGroupID: "1d37-def", KeyName: "xui-ir", DiskGB: 25,
	})
	if err != nil || id != "d0d4-new" {
		t.Fatalf("create: %q, %v", id, err)
	}
	reqs := rs.requests()
	var key map[string]any
	if err := json.Unmarshal(reqs[0].body, &key); err != nil || key["name"] != "xui-ir" || key["public_key"] != "ssh-ed25519 AAAA x-ui" {
		t.Fatalf("key body %s", reqs[0].body)
	}
	var got map[string]any
	if err := json.Unmarshal(reqs[1].body, &got); err != nil {
		t.Fatal(err)
	}
	want := `{"count":1,"disk_size":25,"flavor_id":"eco-1-1-0","ha_enabled":false,"image_id":"f1cc-u24","key_name":"xui-ir","name":"ir","network_ids":["e914-net"],"security_groups":[{"name":"1d37-def"}],"ssh_key":true}`
	if canon, _ := json.Marshal(got); string(canon) != want {
		t.Fatalf("create body\n %s\nwant\n %s", canon, want)
	}
}

func TestACreateReplyIsReadWhicheverShapeItTakes(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		err              error
	}{
		{"one object", `{"data":{"id":"a1"}}`, "a1", nil},
		{"a list of one", `{"data":[{"id":"b2"}]}`, "b2", nil},
		{"no id", `{"data":{"name":"ir"}}`, "", ErrNoServerID},
	} {
		c, _ := replay(t, "k", map[string]reply{"POST /ecc/v1/regions/z/servers": {body: tc.body}})
		id, err := c.CreateServer(context.Background(), "z", NewServer{Name: "ir", SizeID: "s", ImageID: "i", NetworkID: "n"})
		if id != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("%s: %q, %v; want %q, %v", tc.name, id, err, tc.want, tc.err)
		}
	}
	c, _ := replay(t, "k", map[string]reply{"POST /ecc/v1/regions/z/servers": {status: 402, body: `{"message":"Insufficient balance"}`}})
	_, err := c.CreateServer(context.Background(), "z", NewServer{Name: "ir", SizeID: "s", ImageID: "i", NetworkID: "n"})
	var api *APIError
	if !errors.As(err, &api) || api.Status != 402 {
		t.Fatalf("a refused create: %v; want the 402", err)
	}
}

func TestPublicIPv4SkipsPrivateAndIPv6(t *testing.T) {
	s := Server{Addresses: []Address{
		{IP: "10.0.0.5", Version: "4", Public: false},
		{IP: "2a03:ef42::1", Version: "6", Public: true},
		{IP: "37.152.187.164", Version: "4", Public: true},
	}}
	if got := s.PublicIPv4(); got != "37.152.187.164" {
		t.Fatalf("PublicIPv4 = %q", got)
	}
	if got := (Server{}).PublicIPv4(); got != "" {
		t.Fatalf("no address gave %q", got)
	}
}

// A delete goes to the server's own path with forceDelete, as the Terraform provider sends it.
func TestAServerIsDeletedByItsPath(t *testing.T) {
	c, rs := replay(t, "k", map[string]reply{"DELETE /ecc/v1/regions/ir-thr-fr1/servers/d0d4?forceDelete=true": {status: 204}})
	if err := c.DeleteServer(context.Background(), "ir-thr-fr1", "d0d4"); err != nil {
		t.Fatal(err)
	}
	if n := len(rs.requests()); n != 1 {
		t.Fatalf("%d requests", n)
	}
	if err := c.DeleteServer(context.Background(), "ir-thr-fr1", ""); err == nil {
		t.Fatal("a delete with no id was sent")
	}
}

// A new A record carries the fields records made in Arvan's own panel carry.
func TestANewARecordLooksLikeOneArvanMakes(t *testing.T) {
	c, rs := replay(t, "k", map[string]reply{"POST /cdn/4.0/domains/aliwoodbaf.ir/dns-records": {status: 201, body: `{"data":{"id":"r1"}}`}})
	if err := c.CreateARecord(context.Background(), "aliwoodbaf.ir", "ir2", "37.152.190.9"); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(rs.requests()[0].body, &got); err != nil {
		t.Fatal(err)
	}
	want := `{"cloud":false,"ip_filter_mode":{"count":"single","geo_filter":"none","order":"none"},"name":"ir2","ttl":120,"type":"a","upstream_https":"default","value":[{"country":"","ip":"37.152.190.9","port":null,"weight":100}]}`
	if canon, _ := json.Marshal(got); string(canon) != want {
		t.Fatalf("body\n %s\nwant\n %s", canon, want)
	}
	if err := c.CreateARecord(context.Background(), "aliwoodbaf.ir", "ir2", "2001:db8::1"); err == nil {
		t.Fatal("an IPv6 address was written into an A record")
	}
}
