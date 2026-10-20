package openvpn

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/ocserv"
)

// The customer types one name and one password whichever protocol they use.
// That is a promise to them, not a convenience for us, so the hashing, the
// login-name substitution and the validity rules are the cisco ones rather
// than a second set that could drift away from them — see internal/ocserv.
//
// The file itself is login:hash, one per line. OpenVPN cannot read it: it
// hands the credentials to a helper and believes the helper's answer, and the
// helper is this panel's own binary.

// LoginFor turns a client email into the name they type. Shared with cisco so
// a customer with both protocols types the same thing into both.
func LoginFor(email string) string { return ocserv.LoginFor(email) }

// ValidLogin reports whether a login can be written to the credentials file.
func ValidLogin(login string) error { return ocserv.ValidLogin(login) }

// HashPassword produces the stored form. bcrypt, same cost as cisco.
func HashPassword(password string) (string, error) { return ocserv.HashPassword(password) }

// renderPasswd builds the credentials file: one line per client, sorted so
// the file is stable to read and to diff. A client whose name or password
// cannot be used is skipped with a reason rather than taking the file down
// and every other customer with it.
func renderPasswd(users []User) (string, []string) {
	sorted := make([]User, len(users))
	copy(sorted, users)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Login < sorted[j].Login })

	var b strings.Builder
	var problems []string
	for _, u := range sorted {
		if err := ValidLogin(u.Login); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		hash, err := HashPassword(u.Password)
		if err != nil {
			problems = append(problems, fmt.Sprintf("client %q: %v", u.Name, err))
			continue
		}
		fmt.Fprintf(&b, "%s:%s\n", u.Login, hash)
	}
	return b.String(), problems
}

// Verify answers the one question OpenVPN asks: may this name and password
// in? It is the whole of what the helper subcommand does, kept here so it is
// testable without a process and without a file descriptor from OpenVPN.
//
// A wrong name and a wrong password are refused identically and without
// saying which: telling the difference is telling a stranger which accounts
// exist.
func Verify(stored string, login, password string) bool {
	if login == "" || password == "" {
		return false
	}
	scanner := bufio.NewScanner(strings.NewReader(stored))
	for scanner.Scan() {
		name, hash, ok := strings.Cut(strings.TrimSpace(scanner.Text()), ":")
		if !ok || name != login {
			continue
		}
		return ocserv.PasswordMatches(hash, password)
	}
	return false
}

// VerifyFile is Verify against the credentials file of one inbound.
func VerifyFile(id int, login, password string) bool {
	body, err := os.ReadFile(passwdPathForID(id))
	if err != nil {
		return false
	}
	return Verify(string(body), login, password)
}

// ReadCredentialFile reads what OpenVPN leaves for the helper: the username
// on the first line and the password on the second, and nothing else.
//
// Anything past the second line is ignored rather than refused, because the
// file is written by OpenVPN and a future version adding a third line should
// not lock every customer out.
func ReadCredentialFile(path string) (login, password string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if len(lines) < 2 {
		return "", "", fmt.Errorf("%s does not hold a username and a password", path)
	}
	return strings.TrimSpace(lines[0]), lines[1], nil
}
