package ocserv

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The hash the panel writes has to be one the system's crypt(3) verifies,
// because that is what ocserv calls. Checking the hash against its own
// password proves the pair, and the $2 prefix is what libxcrypt dispatches on.
func TestHashPasswordProducesAVerifiableCryptHash(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Fatalf("hash %q does not carry a crypt(3) bcrypt prefix", hash)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("correct horse battery")); err != nil {
		t.Fatalf("the hash does not verify its own password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("wrong")); err == nil {
		t.Fatal("the hash verified a different password")
	}
}

// bcrypt ignores everything past 72 bytes. Truncating silently would make two
// different long passwords open the same account, so the long one is refused.
func TestHashPasswordRefusesAPasswordBcryptWouldTruncate(t *testing.T) {
	_, err := HashPassword(strings.Repeat("a", maxPasswordLen+1))
	if err == nil {
		t.Fatal("a password past bcrypt's limit was accepted and would be truncated")
	}
	// The library refuses it too, so asserting only that an error came back
	// would pass with our own check deleted. The message has to be the one
	// that names the limit, which is the whole reason for checking here.
	if !strings.Contains(err.Error(), "the maximum is") {
		t.Fatalf("error %q does not tell the operator what the limit is", err)
	}
	if _, err := HashPassword(strings.Repeat("a", maxPasswordLen)); err != nil {
		t.Fatalf("a password at exactly the limit was refused: %v", err)
	}
}

func TestHashPasswordRefusesAnEmptyPassword(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Fatal("an empty password was accepted")
	}
}

// Every client a reseller owns has an email namespaced "a<adminId>:", and
// ocpasswd is colon-separated. Measured on ocserv 1.2.4: the colon name is
// refused at login while the same line without it authenticates. So the login
// name must lose the colon while the email — the billing identity — keeps it.
func TestLoginForStripsTheResellerNamespaceColon(t *testing.T) {
	login := LoginFor("a9:customer@example.com")
	if strings.Contains(login, ":") {
		t.Fatalf("login %q still carries a colon; ocserv would refuse it", login)
	}
	if err := ValidLogin(login); err != nil {
		t.Fatalf("the derived login is not writable: %v", err)
	}
	if plain := LoginFor("customer@example.com"); plain != "customer@example.com" {
		t.Fatalf("a client with no namespace was rewritten to %q", plain)
	}
}

// The password file is colon-separated and line-based. A name carrying either
// would let one client's entry rewrite another's, which is an account takeover
// spelled as a naming convention.
func TestValidLoginRejectsSeparators(t *testing.T) {
	for _, bad := range []string{"", "a:b", "a\nb", "a\rb", "victim:*:$2a$10$hash"} {
		if err := ValidLogin(bad); err == nil {
			t.Fatalf("login %q was accepted into the password file", bad)
		}
	}
	if err := ValidLogin("a1_customer@example.com"); err != nil {
		t.Fatalf("an ordinary login name was rejected: %v", err)
	}
}

func TestRenderPasswdWritesOneSortedLinePerClient(t *testing.T) {
	out, problems := renderPasswd([]User{
		{Name: "zoe", Login: "zoe", Password: "zoepass"},
		{Name: "ann", Login: "ann", Password: "annpass"},
	})
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "ann:*:") || !strings.HasPrefix(lines[1], "zoe:*:") {
		t.Fatalf("lines are not sorted login:*:hash:\n%s", out)
	}
	hash := strings.TrimPrefix(lines[0], "ann:*:")
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("annpass")); err != nil {
		t.Fatalf("the written hash does not match the client's password: %v", err)
	}
}

// The line written is the login name, never the email, or the reseller's
// customers would all land in a file ocserv refuses to authenticate against.
func TestRenderPasswdWritesTheLoginNotTheEmail(t *testing.T) {
	out, problems := renderPasswd([]User{
		{Name: "a9:customer@example.com", Login: LoginFor("a9:customer@example.com"), Password: "pw"},
	})
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if !strings.HasPrefix(out, "a9_customer@example.com:*:") {
		t.Fatalf("the line does not start with the login name:\n%s", out)
	}
	if strings.Contains(out, "a9:customer") {
		t.Fatalf("the email was written instead of the login:\n%s", out)
	}
}

// One unusable client must not take the whole file down with it: the rest of
// the inbound's customers keep working and the reason is reported.
func TestRenderPasswdSkipsOnlyTheUnusableClient(t *testing.T) {
	out, problems := renderPasswd([]User{
		{Name: "good", Login: "good", Password: "goodpass"},
		{Name: "bad", Login: "bad:name", Password: "x"},
		{Name: "toolong", Login: "toolong", Password: strings.Repeat("p", maxPasswordLen+1)},
	})
	if len(problems) != 2 {
		t.Fatalf("got %d problems, want 2: %v", len(problems), problems)
	}
	if !strings.HasPrefix(out, "good:*:") || strings.Count(out, "\n") != 1 {
		t.Fatalf("the usable client was not written alone:\n%s", out)
	}
}

// Each render salts afresh, so two renders of the same clients differ byte for
// byte. That is why the manager compares desired plaintext rather than
// rendered output — comparing files would restart ocserv on every reconcile.
func TestRenderPasswdIsNotByteStableAcrossRenders(t *testing.T) {
	users := []User{{Name: "ann", Login: "ann", Password: "annpass"}}
	first, _ := renderPasswd(users)
	second, _ := renderPasswd(users)
	if first == second {
		t.Fatal("two renders matched; if this ever becomes true the fingerprint could compare files instead")
	}
}
