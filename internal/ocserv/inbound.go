package ocserv

import (
	"encoding/json"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// ciscoSettings is the stored shape of a cisco inbound's settings.
type ciscoSettings struct {
	Clients []struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Enable   bool   `json:"enable"`
		// The panel's per-client "IP limit". For every other protocol it caps
		// concurrent addresses through xray's statistics; cisco traffic never
		// reaches xray, so here it caps concurrent sessions in ocserv itself.
		// Same promise to the operator, enforced where it can be.
		LimitIP int `json:"limitIp"`
	} `json:"clients"`
	Network        string   `json:"network"`
	Netmask        string   `json:"netmask"`
	DNS            []string `json:"dns"`
	MaxClients     int      `json:"maxClients"`
	MaxSameClients int      `json:"maxSameClients"`
	CertFile       string   `json:"certFile"`
	KeyFile        string   `json:"keyFile"`
}

// InstanceFromInbound derives the desired ocserv instance from a cisco
// inbound, serving one account per enabled client. Returns false when the
// inbound is not a usable cisco inbound or has no client to serve: an ocserv
// nobody can log into is a port held open for nothing.
func InstanceFromInbound(ib *model.Inbound) (Instance, bool) {
	if ib == nil || ib.Protocol != model.Cisco {
		return Instance{}, false
	}
	var parsed ciscoSettings
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		return Instance{}, false
	}

	users := make([]User, 0, len(parsed.Clients))
	// Two emails can collapse onto one login name once the colon is
	// substituted, and two clients sharing a login name would share an
	// account. The first keeps it and the second is left unserved: visibly
	// broken for one customer, rather than silently shared by two.
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
			logger.Warningf("ocserv: inbound %d: clients %q and %q both log in as %q; only the first is served",
				ib.Id, other, name, login)
			continue
		}
		taken[login] = name
		users = append(users, User{Name: name, Login: login, Password: c.Password, DeviceLimit: c.LimitIP})
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
		CertFile:       strings.TrimSpace(parsed.CertFile),
		KeyFile:        strings.TrimSpace(parsed.KeyFile),
	}, true
}
