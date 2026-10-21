package filestamp

import (
	"os"
	"path/filepath"
	"testing"
)

// A certificate renewed at the same path is a different certificate. The
// stamp has to say so, and has to stay put while nothing changes, or every
// reconcile would restart the sidecar and drop everyone on it.
func TestTheStampFollowsTheContentNotThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fullchain.pem")
	if err := os.WriteFile(path, []byte("old certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := Of(path)
	if first == "" || first == "missing" {
		t.Fatalf("a readable file stamped as %q", first)
	}
	if again := Of(path); again != first {
		t.Fatalf("an untouched file changed stamp: %q then %q", first, again)
	}
	if err := os.WriteFile(path, []byte("renewed certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if renewed := Of(path); renewed == first {
		t.Fatal("a file replaced in place kept its stamp")
	}
}

func TestNoPathAndAMissingFileAreTellableApart(t *testing.T) {
	if got := Of("  "); got != "" {
		t.Errorf("no path stamped as %q, want empty", got)
	}
	if got := Of(filepath.Join(t.TempDir(), "absent.pem")); got != "missing" {
		t.Errorf("an absent file stamped as %q, want missing", got)
	}
}
