package ocserv

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is deliberately modest: ocserv hashes on every authentication, and
// a reconnecting phone must not wait on a deliberately slow KDF.
const bcryptCost = 10

// maxPasswordLen is bcrypt's own limit. Everything past 72 bytes is silently
// ignored by the algorithm, which would make two different passwords open the
// same account, so a long one is refused instead of quietly truncated.
const maxPasswordLen = 72

// User is one client served by an ocserv instance.
//
// Name is the panel's identity for the client — its email — and is what
// traffic is billed against. Login is what the customer actually types into
// their AnyConnect client, and is not always the same string: see LoginFor.
type User struct {
	Name     string
	Login    string
	Password string
	// DeviceLimit caps how many devices may hold this account at once. Zero
	// leaves the client on the inbound's own limit. ocserv enforces it through
	// a per-user config file, the only place the limit can be set per client
	// rather than per inbound.
	DeviceLimit int

	// Locations are the countries this client may pick from the dropdown.
	// Empty means the inbound offers none, and the client connects to the
	// one pool there is. It must not mean "entitled to nothing" on an
	// inbound that does offer countries: measured on ocserv 1.2.4, such a
	// client cannot connect at all.
	Locations []string
}

// LoginFor turns a client email into a name the password file can hold.
//
// ocpasswd is colon-separated, so a client whose email carries a colon cannot
// be written at all — and every client a reseller owns carries one, because
// client emails are namespaced "a<adminId>:". Measured against ocserv 1.2.4: a
// user line for "a9:mine@example.com" is refused at login while an identical
// line without the colon authenticates, so this is a substitution, not a
// precaution. The email stays the identity everywhere else; only the login
// name is rewritten.
func LoginFor(email string) string {
	return strings.ReplaceAll(email, ":", "_")
}

// HashPassword produces a crypt(3) hash ocserv's plain authentication accepts.
//
// bcrypt is used rather than the SHA-512 crypt that ocserv's own ocpasswd tool
// writes, because x/crypto/bcrypt is already a dependency: the alternatives
// were a new dependency, hand-rolled crypto, or shelling out to openssl for
// every password. ocserv verifies through the system crypt(3), and libxcrypt
// accepts the $2a$ prefix Go emits alongside $2b$ and $2y$.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password is empty")
	}
	if len(password) > maxPasswordLen {
		return "", fmt.Errorf("password is %d bytes; the maximum is %d", len(password), maxPasswordLen)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// groupField renders a client's countries for the password file. ocserv reads
// the field as a comma-separated list, and "*" when there is nothing to list.
func groupField(locations []string) string {
	names := make([]string, 0, len(locations))
	seen := make(map[string]struct{}, len(locations))
	for _, name := range locations {
		name = strings.TrimSpace(name)
		if ValidLocationName(name) != nil {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return "*"
	}
	return strings.Join(names, ",")
}

// ValidLogin reports whether a login name can be written to the password file.
// The file is colon-separated and line-based, so a name carrying either would
// let one client's entry rewrite another's.
func ValidLogin(login string) error {
	if login == "" {
		return fmt.Errorf("client login name is empty")
	}
	if strings.ContainsAny(login, ":\n\r") {
		return fmt.Errorf("client login name %q contains a character the password file cannot hold (: or a line break)", login)
	}
	return nil
}

// renderPasswd builds the ocpasswd file for an instance: one line per client,
// as login:groups:hash. Clients are sorted so the file is stable to read and
// to diff; a client whose name or password cannot be used is skipped with a
// reason rather than taking the whole file down.
//
// The group field is the comma-separated list of countries that client may
// pick, or "*" when the inbound offers none. "*" is not "any group": measured
// on ocserv 1.2.4, a user written with "*" who asks for a named group is
// refused with "requested group ... but is not a member", and once any
// select-group exists that same user cannot connect without naming one
// either. On an inbound with countries, every client therefore carries a real
// list.
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
		fmt.Fprintf(&b, "%s:%s:%s\n", u.Login, groupField(u.Locations), hash)
	}
	return b.String(), problems
}
