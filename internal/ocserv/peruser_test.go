package ocserv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readPerUser(t *testing.T, id int, login string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(perUserDirForID(id), login))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// A reseller sells plans with different device counts, so the limit has to be
// per client. ocserv can only express that through a per-user config file:
// max-same-clients in the main config is one number for the whole inbound.
func TestPerClientDeviceLimitIsWrittenForTheClientsThatHaveOne(t *testing.T) {
	withTempRuntime(t)
	inst := Instance{Id: 4, Users: []User{
		{Name: "one", Login: "one", Password: "p", DeviceLimit: 1},
		{Name: "three", Login: "three", Password: "p", DeviceLimit: 3},
		{Name: "unlimited", Login: "unlimited", Password: "p"},
	}}
	if err := writePerUserLimits(inst); err != nil {
		t.Fatalf("writePerUserLimits: %v", err)
	}

	body, ok := readPerUser(t, 4, "one")
	if !ok || !strings.Contains(body, "max-same-clients = 1") {
		t.Fatalf("client with a limit of 1 got %q (present=%v)", body, ok)
	}
	if body, ok := readPerUser(t, 4, "three"); !ok || !strings.Contains(body, "max-same-clients = 3") {
		t.Fatalf("client with a limit of 3 got %q (present=%v)", body, ok)
	}
	// No file means "fall through to the inbound's own limit". Writing one
	// with 0 would cap the client at zero devices, locking them out.
	if _, ok := readPerUser(t, 4, "unlimited"); ok {
		t.Fatal("a client with no limit got a per-user file, which would override the inbound default")
	}
}

// Clearing a limit has to delete the file. A stale one keeps capping a client
// whose limit the operator has just removed, and nothing would show why.
func TestClearingADeviceLimitRemovesItsFile(t *testing.T) {
	withTempRuntime(t)
	capped := Instance{Id: 5, Users: []User{{Name: "ann", Login: "ann", Password: "p", DeviceLimit: 2}}}
	if err := writePerUserLimits(capped); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, ok := readPerUser(t, 5, "ann"); !ok {
		t.Fatal("test setup: the limit file was not written")
	}

	cleared := Instance{Id: 5, Users: []User{{Name: "ann", Login: "ann", Password: "p"}}}
	if err := writePerUserLimits(cleared); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if body, ok := readPerUser(t, 5, "ann"); ok {
		t.Fatalf("the cleared limit is still on disk: %q", body)
	}
}

// A client removed from the inbound must not leave its limit behind for
// whoever takes that login name next.
func TestARemovedClientLeavesNoLimitFile(t *testing.T) {
	withTempRuntime(t)
	before := Instance{Id: 6, Users: []User{
		{Name: "ann", Login: "ann", Password: "p", DeviceLimit: 1},
		{Name: "bob", Login: "bob", Password: "p", DeviceLimit: 1},
	}}
	if err := writePerUserLimits(before); err != nil {
		t.Fatalf("first write: %v", err)
	}
	after := Instance{Id: 6, Users: []User{{Name: "ann", Login: "ann", Password: "p", DeviceLimit: 1}}}
	if err := writePerUserLimits(after); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if _, ok := readPerUser(t, 6, "bob"); ok {
		t.Fatal("a removed client's limit file survived")
	}
	if _, ok := readPerUser(t, 6, "ann"); !ok {
		t.Fatal("the remaining client's limit was deleted too")
	}
}

// ocserv only reads the per-user directory when the main config points at it.
func TestRenderConfigPointsAtThePerUserDirectory(t *testing.T) {
	cfg := configFor(t, Instance{Id: 7, Port: 443})
	if !strings.Contains(cfg, "config-per-user = ") {
		t.Fatalf("config does not enable per-user overrides:\n%s", cfg)
	}
}

// A changed device limit has to reach the file, which only happens when the
// user fingerprint moves — the manager rewrites nothing when it has not.
func TestUsersFingerprintMovesWithADeviceLimit(t *testing.T) {
	a := Instance{Id: 1, Users: []User{{Name: "ann", Login: "ann", Password: "p", DeviceLimit: 1}}}
	b := Instance{Id: 1, Users: []User{{Name: "ann", Login: "ann", Password: "p", DeviceLimit: 3}}}
	if a.usersFingerprint() == b.usersFingerprint() {
		t.Fatal("changing a device limit left the fingerprint unchanged, so the file would never be rewritten")
	}
}

// The limit is a per-client change, and ocserv reads the file at
// authentication time, so it must not restart the instance and drop everyone
// else who is connected.
func TestADeviceLimitChangeDoesNotRestartTheInstance(t *testing.T) {
	a := Instance{Id: 1, Port: 443, Users: []User{{Name: "ann", Login: "ann", Password: "p", DeviceLimit: 1}}}
	b := Instance{Id: 1, Port: 443, Users: []User{{Name: "ann", Login: "ann", Password: "p", DeviceLimit: 5}}}

	got := ensureActionFor(true, a.structuralFingerprint(), a.usersFingerprint(),
		b.structuralFingerprint(), b.usersFingerprint())
	if got != ensureUsers {
		t.Fatalf("ensureActionFor = %d, want ensureUsers: a restart would drop every other client", got)
	}
}
