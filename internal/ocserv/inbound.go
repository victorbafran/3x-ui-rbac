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
		// Locations are the countries this client may pick. Empty means
		// every country the inbound offers: turning locations on must not
		// lock out the clients that were there before them, and on an
		// inbound with countries a client entitled to none cannot connect
		// at all.
		Locations []string `json:"locations"`
	} `json:"clients"`
	Network        string   `json:"network"`
	Netmask        string   `json:"netmask"`
	DNS            []string `json:"dns"`
	MaxClients     int      `json:"maxClients"`
	MaxSameClients int      `json:"maxSameClients"`
	UDPPort        int      `json:"udpPort"`
	DisableUDP     bool     `json:"disableUdp"`
	MTU            int      `json:"mtu"`
	MTUDiscovery   bool     `json:"mtuDiscovery"`
	MobileDPD      int      `json:"mobileDpd"`
	CertFile       string   `json:"certFile"`
	KeyFile        string   `json:"keyFile"`
	// Locations are the countries this inbound offers in the client's Group
	// dropdown. None means the single pool this inbound has always had.
	Locations []struct {
		Name    string   `json:"name"`
		Label   string   `json:"label"`
		Network string   `json:"network"`
		Netmask string   `json:"netmask"`
		DNS     []string `json:"dns"`
		Exit    string   `json:"exit"`
	} `json:"locations"`
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

	locations := make([]Location, 0, len(parsed.Locations))
	offered := make([]string, 0, len(parsed.Locations))
	for _, l := range parsed.Locations {
		name := strings.TrimSpace(l.Name)
		if err := ValidLocationName(name); err != nil {
			logger.Warningf("ocserv: inbound %d: %v; that location is not offered", ib.Id, err)
			continue
		}
		dns := make([]string, 0, len(l.DNS))
		for _, d := range l.DNS {
			if d = strings.TrimSpace(d); d != "" {
				dns = append(dns, d)
			}
		}
		locations = append(locations, Location{
			Name:    name,
			Label:   strings.TrimSpace(l.Label),
			Network: strings.TrimSpace(l.Network),
			Netmask: strings.TrimSpace(l.Netmask),
			DNS:     dns,
			Exit:    strings.TrimSpace(l.Exit),
		})
		offered = append(offered, name)
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
		users = append(users, User{
			Name:        name,
			Login:       login,
			Password:    c.Password,
			DeviceLimit: c.LimitIP,
			Locations:   entitlement(c.Locations, offered),
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
		UDPPort:        parsed.UDPPort,
		DisableUDP:     parsed.DisableUDP,
		MTU:            parsed.MTU,
		MTUDiscovery:   parsed.MTUDiscovery,
		MobileDPD:      parsed.MobileDPD,
		Locations:      locations,
		CertFile:       strings.TrimSpace(parsed.CertFile),
		KeyFile:        strings.TrimSpace(parsed.KeyFile),
	}, true
}

// DTLSPort is the UDP port a cisco inbound holds, zero with DTLS off. Settings
// alone: an inbound with no client yet still owns the port its first one opens.
func DTLSPort(ib *model.Inbound) int {
	if ib == nil || ib.Protocol != model.Cisco {
		return 0
	}
	var parsed struct {
		UDPPort    int  `json:"udpPort"`
		DisableUDP bool `json:"disableUdp"`
	}
	_ = json.Unmarshal([]byte(ib.Settings), &parsed)
	if parsed.DisableUDP {
		return 0
	}
	return Instance{Port: ib.Port, UDPPort: parsed.UDPPort}.udpPort()
}

// entitlement works out which countries one client may pick.
//
// A client that names none gets every country the inbound offers. That is the
// difference between switching locations on and cutting off every customer who
// was connecting before they existed: measured on ocserv 1.2.4, once any
// select-group is configured, a client with no group cannot connect at all.
// A client that names some keeps only the ones still offered, so deleting a
// country does not leave a stale name in a password file.
func entitlement(wanted, offered []string) []string {
	if len(offered) == 0 {
		return nil
	}
	if len(wanted) == 0 {
		return offered
	}
	have := make(map[string]struct{}, len(offered))
	for _, name := range offered {
		have[name] = struct{}{}
	}
	out := make([]string, 0, len(wanted))
	for _, name := range wanted {
		name = strings.TrimSpace(name)
		if _, ok := have[name]; ok {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return offered
	}
	return out
}
