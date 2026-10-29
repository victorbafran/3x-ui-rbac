package job

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// The job is the only thing that saves Psiphon's traffic while the panel runs, a country
// switched off since its last bytes included; without it every count waited for shutdown.
func TestThePsiphonJobSavesTheTrafficWithNoCountryOn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dir)
	dbtest.InitDB(t, filepath.Join(dir, "x-ui.db"))
	row := model.PsiphonCountry{Code: "DE", Enabled: false, SocksPort: 11181, HTTPPort: 11381, TrafficDown: 1000, TrafficUp: 10}
	if err := database.GetDB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	psiphon.GetManager().CountForTest("DE", psiphon.Traffic{Down: 2524300, Up: 722})

	NewPsiphonJob().Run()

	if err := database.GetDB().Where("code = ?", "DE").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.TrafficDown != 1000+2524300 || row.TrafficUp != 10+722 {
		t.Fatalf("DE holds %d down, %d up; want %d, %d", row.TrafficDown, row.TrafficUp, 1000+2524300, 10+722)
	}
}
