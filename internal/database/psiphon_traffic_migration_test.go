package database

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The table as the first Psiphon build made it, read off the panel box (2026-10-29).
var legacyPsiphonCountriesDDL = []string{
	"CREATE TABLE `psiphon_countries` (`code` text,`enabled` numeric NOT NULL DEFAULT false,`socks_port` integer NOT NULL,`http_port` integer NOT NULL,`exit_ip` text NOT NULL DEFAULT \"\",`exit_loc` text NOT NULL DEFAULT \"\",`last_check` integer,`last_error` text NOT NULL DEFAULT \"\",`created_at` integer,PRIMARY KEY (`code`))",
	"CREATE UNIQUE INDEX `idx_psiphon_countries_http_port` ON `psiphon_countries`(`http_port`)",
	"CREATE UNIQUE INDEX `idx_psiphon_countries_socks_port` ON `psiphon_countries`(`socks_port`)",
}

// A panel upgraded with countries running keeps them, counting from zero; a
// NOT NULL counter without a default would stop the upgraded panel starting.
func TestPsiphonCountriesGainTrafficCountersOnUpgrade(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	legacy, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	for _, ddl := range legacyPsiphonCountriesDDL {
		if err := legacy.Exec(ddl).Error; err != nil {
			t.Fatalf("create legacy psiphon_countries: %v", err)
		}
	}
	if err := legacy.Exec(
		`INSERT INTO psiphon_countries (code, enabled, socks_port, http_port, exit_ip, exit_loc, last_check, last_error, created_at)
		 VALUES ('DE', 1, 11181, 11381, '164.92.130.120', 'DE', 1793270000, '', 1793260000)`,
	).Error; err != nil {
		t.Fatalf("seed legacy country: %v", err)
	}
	sqlDB, err := legacy.DB()
	if err != nil {
		t.Fatalf("legacy db handle: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB over the legacy psiphon_countries: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	var row model.PsiphonCountry
	if err := GetDB().Where("code = ?", "DE").First(&row).Error; err != nil {
		t.Fatalf("the running country was lost: %v", err)
	}
	want := model.PsiphonCountry{
		Code: "DE", Enabled: true, SocksPort: 11181, HTTPPort: 11381, ExitIP: "164.92.130.120", ExitLoc: "DE",
		LastCheck: 1793270000, CreatedAt: 1793260000,
	}
	if row != want {
		t.Fatalf("after the upgrade DE is %+v, want %+v", row, want)
	}
}
