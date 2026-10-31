package sub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/openvpn"
)

// profileErrorResponse renders the profile the way subOpenVPN does and answers
// with whatever went wrong, on a host where openvpn is or is not installed.
func profileErrorResponse(t *testing.T, installed bool, keysOnDisk bool, address string) *httptest.ResponseRecorder {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XUI_OPENVPN_DIR", dir)
	if keysOnDisk {
		for _, name := range []string{"ca-3.pem", "tls-crypt-3.key"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	saved := openvpnAvailable
	openvpnAvailable = func() error {
		if installed {
			return nil
		}
		return openvpn.ErrNotInstalled
	}
	t.Cleanup(func() { openvpnAvailable = saved })

	_, err := openvpn.Render(openvpn.Instance{Id: 3, Port: 1194}, openvpn.Profile{Address: address})
	if err == nil {
		t.Fatal("the profile rendered; this test needs one that cannot")
	}
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	writeOpenVPNProfileError(c, err)
	return rec
}

func reasonOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the body is not the reason JSON: %q", rec.Body.String())
	}
	return body.Reason
}

// A fresh server had no openvpn, and the page told the customer to try again in
// a minute, forever. Only an inbound that can still start is worth waiting for.
func TestTheProfileSaysWhyItCannotBeMade(t *testing.T) {
	cases := []struct {
		name       string
		installed  bool
		keysOnDisk bool
		address    string
		status     int
		reason     string
		retryAfter string
	}{
		{"not started yet", true, false, "vpn.example.com", http.StatusServiceUnavailable, "notStarted", "60"},
		{"openvpn not installed", false, false, "vpn.example.com", http.StatusServiceUnavailable, "notInstalled", ""},
		{"no address", true, true, "", http.StatusInternalServerError, "noAddress", ""},
		{"a broken address", true, true, "vpn.example.com\nup /bin/sh", http.StatusInternalServerError, "invalid", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := profileErrorResponse(t, tc.installed, tc.keysOnDisk, tc.address)
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := reasonOf(t, rec); got != tc.reason {
				t.Errorf("reason = %q, want %q", got, tc.reason)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.retryAfter {
				t.Errorf("Retry-After = %q, want %q", got, tc.retryAfter)
			}
		})
	}
}
