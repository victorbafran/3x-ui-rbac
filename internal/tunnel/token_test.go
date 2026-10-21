package tunnel

import "testing"

// The token is open to the whole internet on the tunnel port, so it must be
// long and random, never short enough to type or guess.
func TestTokenIsLongRandomAndUrlSafe(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if len(tok) < 40 {
			t.Fatalf("token %q is short enough to guess", tok)
		}
		if seen[tok] {
			t.Fatalf("NewToken repeated %q within 100 draws", tok)
		}
		seen[tok] = true
		// URL-safe base64 so it survives a TOML string and a shell argument
		// without quoting surprises: only these characters may appear.
		for _, c := range tok {
			ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_'
			if !ok {
				t.Fatalf("token %q has a character that needs quoting: %q", tok, c)
			}
		}
	}
}
