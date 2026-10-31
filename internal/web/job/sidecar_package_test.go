package job

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/op/go-logging"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	xuilogger "github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/ocserv"
	"github.com/mhsanaei/3x-ui/v3/internal/openvpn"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// A fresh server had no openvpn, so an enabled OpenVPN inbound never started;
// a tick that has an inbound to serve asks for the package, one with none does not.
func TestATickWithAnInboundToServeAsksForItsPackage(t *testing.T) {
	xuilogger.InitLogger(logging.ERROR)
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	savedCisco, savedOpenVPN, savedRequest := ciscoManager, openvpnManager, requestPackage
	t.Cleanup(func() { ciscoManager, openvpnManager, requestPackage = savedCisco, savedOpenVPN, savedRequest })
	ciscoManager = func() ciscoSidecars { return &fakeSidecars[ocserv.Instance, ocserv.Traffic]{} }
	openvpnManager = func() openvpnSidecars { return &fakeSidecars[openvpn.Instance, openvpn.Traffic]{} }
	var asked []string
	requestPackage = func(p sidecar.Package) { asked = append(asked, p.Name) }

	NewCiscoJob().Run()
	NewOpenVPNJob().Run()
	if len(asked) != 0 {
		t.Fatalf("with no inbound the ticks asked for %q", asked)
	}

	client := `{"clients":[{"email":"a@example.com","password":"pw123456","enable":true}]}`
	for _, ib := range []*model.Inbound{
		{Tag: "cisco-443", Port: 443, Protocol: model.Cisco, Enable: true, Settings: client},
		{Tag: "ovpn-1194", Port: 1194, Protocol: model.OpenVPN, Enable: true, Settings: client},
	} {
		if err := database.GetDB().Create(ib).Error; err != nil {
			t.Fatal(err)
		}
	}
	NewCiscoJob().Run()
	NewOpenVPNJob().Run()
	if want := []string{"ocserv", "openvpn"}; !slices.Equal(asked, want) {
		t.Fatalf("the ticks asked for %q, want %q", asked, want)
	}
}
