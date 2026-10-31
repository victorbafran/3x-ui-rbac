package openvpn

import (
	"encoding/json"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/sidecar"
)

// openvpnSettings is the stored shape of an OpenVPN inbound's settings.
//
// It deliberately mirrors the cisco one. A customer who has both protocols
// should see the same fields mean the same things, and an operator who has
// set up one should not have to learn the other.
type openvpnSettings struct {
	Clients []struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Enable   bool   `json:"enable"`
		// The panel's per-client "IP limit". For every other protocol it caps
		// concurrent addresses through xray's statistics; OpenVPN traffic
		// never reaches xray, so here it caps concurrent devices through the
		// server's own management interface. Same promise to the operator,
		// enforced where it can be.
		LimitIP int `json:"limitIp"`
	} `json:"clients"`
	Network    string   `json:"network"`
	Netmask    string   `json:"netmask"`
	DNS        []string `json:"dns"`
	MaxClients int      `json:"maxClients"`
	// MaxSameClients is how many devices one account may hold at once.
	MaxSameClients int `json:"maxSameClients"`
	// UseTCP serves the inbound over TCP. UDP is faster and is the default;
	// TCP is for a network that drops UDP outright.
	UseTCP bool `json:"useTcp"`
	// Exit is the interface this inbound's traffic leaves through — in
	// practice a tunnel to one of the panel's exit servers. Empty leaves
	// through this host, which is the right answer for the country the
	// server is already in.
	Exit string `json:"exit"`
	// certFile/keyFile, which an older form saved, are left unread: the
	// panel's own authority is the only one a customer's file can carry.
}

// UsesTCP says whether this inbound is served over TCP rather than UDP.
//
// The customer's own file has to name the same transport the server listens
// on. A profile that says udp against a server listening on tcp does not fail
// with a wrong-protocol error; it sits there until it times out, which reads
// to the customer as a dead server.
func UsesTCP(ib *model.Inbound) bool {
	if ib == nil || ib.Protocol != model.OpenVPN {
		return false
	}
	var parsed openvpnSettings
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		return false
	}
	return parsed.UseTCP
}

// ExitOf is the interface an OpenVPN inbound's traffic leaves through, empty
// for this host itself.
func ExitOf(ib *model.Inbound) string {
	if ib == nil || ib.Protocol != model.OpenVPN {
		return ""
	}
	var parsed openvpnSettings
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Exit)
}

// InstanceFromInbound derives the sidecar config for an OpenVPN inbound,
// serving one account per enabled client. Returns false when the inbound is
// not a usable OpenVPN inbound or has no client to serve: a server nobody can
// log into is a port held open for nothing.
func InstanceFromInbound(ib *model.Inbound) (Instance, bool) {
	if ib == nil || ib.Protocol != model.OpenVPN {
		return Instance{}, false
	}
	var parsed openvpnSettings
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		return Instance{}, false
	}

	users := make([]User, 0, len(parsed.Clients))
	// Two emails can collapse onto one login once the colon is substituted,
	// and two clients sharing a login would share an account. The first keeps
	// it and the second is left unserved: visibly broken for one customer,
	// rather than silently shared by two.
	taken := make(map[string]string, len(parsed.Clients))
	for _, c := range parsed.Clients {
		name := strings.TrimSpace(c.Email)
		if !c.Enable || name == "" || c.Password == "" {
			continue
		}
		login := LoginFor(name)
		if ValidLogin(login) != nil {
			continue
		}
		if other, clash := taken[login]; clash {
			logger.Warningf("openvpn: inbound %d: clients %q and %q both log in as %q; only the first is served",
				ib.Id, other, name, login)
			continue
		}
		taken[login] = name
		users = append(users, User{
			Name:        name,
			Login:       login,
			Password:    c.Password,
			DeviceLimit: c.LimitIP,
		})
	}
	if len(users) == 0 {
		return Instance{}, false
	}

	dns := make([]string, 0, len(parsed.DNS))
	for _, d := range parsed.DNS {
		if d = strings.TrimSpace(d); d != "" {
			dns = append(dns, d)
		}
	}
	return Instance{
		Id:             ib.Id,
		Tag:            ib.Tag,
		Listen:         ib.Listen,
		Port:           ib.Port,
		Users:          users,
		Network:        strings.TrimSpace(parsed.Network),
		Netmask:        strings.TrimSpace(parsed.Netmask),
		DNS:            dns,
		MaxClients:     parsed.MaxClients,
		MaxSameClients: parsed.MaxSameClients,
		UseTCP:         parsed.UseTCP,
		IPv6:           sidecar.IPv6SettingsOf(ib.Settings).ServesIPv6(),
		Exit:           strings.TrimSpace(parsed.Exit),
	}, true
}

// UserNames lists the panel identities this instance serves, which is what
// traffic and depletion are looked up by.
func (inst Instance) UserNames() []string {
	out := make([]string, 0, len(inst.Users))
	for _, u := range inst.Users {
		out = append(out, u.Name)
	}
	return out
}
