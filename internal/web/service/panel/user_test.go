package panel

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
)

// install.sh replaces the credentials only while this reports true, and before
// the first start a database holds no user at all.
func TestHasDefaultCredential(t *testing.T) {
	hash := func(t *testing.T, password string) string {
		t.Helper()
		h, err := crypto.HashPasswordAsBcrypt(password)
		if err != nil {
			t.Fatalf("hash: %v", err)
		}
		return h
	}
	cases := []struct {
		name string
		user *model.User
		want bool
	}{
		{name: "no user before the first start", user: nil, want: true},
		{name: "legacy admin/admin", user: &model.User{Username: "admin", Password: hash(t, "admin")}, want: true},
		{name: "chosen credentials", user: &model.User{Username: "admin", Password: hash(t, "s3cret-Pass")}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
			if err := database.GetDB().Where("1 = 1").Delete(&model.User{}).Error; err != nil {
				t.Fatalf("clear users: %v", err)
			}
			if tc.user != nil {
				if err := database.GetDB().Create(tc.user).Error; err != nil {
					t.Fatalf("create user: %v", err)
				}
			}
			got, err := (&UserService{}).HasDefaultCredential()
			if err != nil || got != tc.want {
				t.Fatalf("HasDefaultCredential() = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

// install.sh sets its credentials before the first start, on a database with no
// user; the panel's owner it creates must be able to run the panel.
func TestUpdateFirstUserOnAFreshDatabaseMakesASuperAdmin(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	if err := (&UserService{}).UpdateFirstUser("owner", "s3cret-Pass"); err != nil {
		t.Fatalf("UpdateFirstUser: %v", err)
	}
	var owner model.User
	if err := database.GetDB().Where("username = ?", "owner").First(&owner).Error; err != nil {
		t.Fatalf("load owner: %v", err)
	}
	if owner.Role != model.RoleSuperAdmin || !owner.Enabled {
		t.Fatalf("owner role = %q enabled = %v, want an enabled %q", owner.Role, owner.Enabled, model.RoleSuperAdmin)
	}
}
