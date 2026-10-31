package service

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// A probe lists only the countries of the servers the client was handed; Poland,
// Singapore and Canada were missing from one and each still connected when picked.
func TestTheKnownCountriesStayOfferedWhateverTheProbeSaw(t *testing.T) {
	setupRelayDB(t)
	bin := filepath.Join(config.GetDBFolderPath(), "tools", "psiphon-"+psiphon.BinaryCommit[:12], "x-ui-psiphon-linux-amd64")
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("client"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(bin)) })
	if err := (&SettingService{}).setString(psiphonRegionsKey, `{"regions":["DE","NL","ZZ"],"at":1}`); err != nil {
		t.Fatal(err)
	}

	st, err := (&PsiphonService{}).State()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range append([]string{"ZZ"}, psiphon.FallbackRegions...) {
		if !slices.Contains(st.Regions, code) {
			t.Errorf("%s is not offered; offered %v", code, st.Regions)
		}
	}
	if !slices.IsSorted(st.Regions) {
		t.Errorf("the countries are not in order: %v", st.Regions)
	}
}
