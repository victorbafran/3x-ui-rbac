package openvpn

import (
	"os/user"
	"strconv"
)

// openvpn drops to an unprivileged account once it has its socket and its tun
// device, and the helper it runs to check a password is spawned *after* that
// drop. So the helper reads the credentials file as the dropped user, not as
// the panel.
//
// Getting that wrong is what rejected every customer on a correct password:
// the file was written root-only on the belief that the panel would be the
// one reading it, openvpn handed the helper to `nobody`, the open failed, and
// a failed open is indistinguishable from a wrong password by the time it
// reaches the client.
//
// So the account to drop to and the group that may read the file are decided
// here, together, once. The two cannot disagree again because there is only
// one answer.

// DropAccount is the unprivileged identity openvpn runs as after startup.
type DropAccount struct {
	User  string
	Group string
	GID   int
}

// lookupGroupForTest lets a test choose the answer without needing an account
// on the machine running it.
var lookupGroupForTest func(name string) (*user.Group, bool)

// candidateGroups are the conventional unprivileged groups, in the order they
// are worth trying. Debian and Ubuntu use nogroup; RHEL and Alpine use
// nobody. A distribution with neither gets no drop at all rather than a
// config naming a group that does not exist, which openvpn refuses to start
// with.
var candidateGroups = []string{"nogroup", "nobody"}

const dropUser = "nobody"

// ResolveDropAccount returns the account openvpn should drop to, or nil when
// there is none to drop to on this host.
//
// nil is not a failure. It means openvpn keeps its privileges and the
// credentials file stays readable only by root, which is consistent and
// works; what does not work is dropping to an account that cannot read the
// file it is about to be asked to read.
func ResolveDropAccount() *DropAccount {
	if _, err := lookupUser(dropUser); err != nil {
		return nil
	}
	for _, name := range candidateGroups {
		g, ok := lookupGroup(name)
		if !ok {
			continue
		}
		gid, err := strconv.Atoi(g.Gid)
		if err != nil {
			continue
		}
		return &DropAccount{User: dropUser, Group: name, GID: gid}
	}
	return nil
}

func lookupUser(name string) (*user.User, error) { return user.Lookup(name) }

func lookupGroup(name string) (*user.Group, bool) {
	if lookupGroupForTest != nil {
		return lookupGroupForTest(name)
	}
	g, err := user.LookupGroup(name)
	if err != nil {
		return nil, false
	}
	return g, true
}
