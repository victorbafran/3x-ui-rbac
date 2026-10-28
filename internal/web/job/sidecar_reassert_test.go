package job

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/op/go-logging"

	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	xuilogger "github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/ocserv"
	"github.com/mhsanaei/3x-ui/v3/internal/openvpn"
)

// fakeSidecars is a sidecar manager with something running that lists what a tick
// asked of it.
type fakeSidecars[I, T any] struct{ calls []string }

func (f *fakeSidecars[I, T]) HasRunning() bool { return true }
func (f *fakeSidecars[I, T]) Reconcile([]I)    { f.calls = append(f.calls, "reconcile") }
func (f *fakeSidecars[I, T]) ReassertRoutes()  { f.calls = append(f.calls, "reassert") }
func (f *fakeSidecars[I, T]) CollectTraffic() ([]T, []string) {
	f.calls = append(f.calls, "collect")
	return nil, nil
}

// A device recreated under a running server (an Xray restart remakes every psi- TUN,
// a relay brought back up its wgrN) takes its route with it, and nothing restarts
// ocserv or OpenVPN to write it again. Each tick puts it back after its reconcile.
func TestEveryTickPutsTheExitRoutesBack(t *testing.T) {
	xuilogger.InitLogger(logging.ERROR)
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	cisco := &fakeSidecars[ocserv.Instance, ocserv.Traffic]{}
	ovpn := &fakeSidecars[openvpn.Instance, openvpn.Traffic]{}
	savedCisco, savedOpenVPN := ciscoManager, openvpnManager
	t.Cleanup(func() { ciscoManager, openvpnManager = savedCisco, savedOpenVPN })
	ciscoManager = func() ciscoSidecars { return cisco }
	openvpnManager = func() openvpnSidecars { return ovpn }

	NewCiscoJob().Run()
	NewOpenVPNJob().Run()

	want := []string{"reconcile", "reassert", "collect"}
	if !slices.Equal(cisco.calls, want) {
		t.Errorf("cisco tick %q, want %q", cisco.calls, want)
	}
	if !slices.Equal(ovpn.calls, want) {
		t.Errorf("OpenVPN tick %q, want %q", ovpn.calls, want)
	}
}
