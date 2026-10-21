package relay

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// addressSources are asked about a given address rather than about whoever is
// asking. Each takes the address in place of %s. ipwho.is and ip-api answered
// for the Iran test front; ipinfo answered nothing for it, so it is last.
var addressSources = []string{
	"https://ipwho.is/%s",
	"http://ip-api.com/json/%s",
	"https://ipinfo.io/%s/json",
}

// WhereIs reports where an address is, asked of the lookup services from this
// panel.
//
// An exit is located by asking it, through SSH, because what matters about an
// exit is where its outgoing traffic appears — that is where its customers
// appear. A front is the opposite: what matters is where customers connect
// to, which is its own address. The two are not always the same place. The
// Iran test front, 185.226.116.154 in Tehran, sends its international traffic
// out through Frankfurt, and asking it named it Germany; asking about its
// address names it Iran.
func WhereIs(ctx context.Context, address string) (Here, error) {
	ip, err := resolveForLookup(ctx, address)
	if err != nil {
		return Here{}, err
	}
	client := &http.Client{Timeout: lookupTimeout}
	for _, pattern := range addressSources {
		if h, ok := askOne(ctx, client, fmt.Sprintf(pattern, url.PathEscape(ip))); ok {
			return h, nil
		}
	}
	return Here{}, fmt.Errorf("no lookup service knew where %s is", address)
}

// resolveForLookup turns a front's address into the one IP the services are
// asked about. A name is resolved, IPv4 first, because that is what a
// customer's client is most likely to reach.
func resolveForLookup(ctx context.Context, address string) (string, error) {
	address = strings.Trim(strings.TrimSpace(address), "[]")
	if address == "" {
		return "", fmt.Errorf("no address to look up")
	}
	if ip := net.ParseIP(address); ip != nil {
		return ip.String(), nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, address)
	if err != nil || len(addrs) == 0 {
		return "", fmt.Errorf("could not resolve %s: %w", address, err)
	}
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return addrs[0].IP.String(), nil
}
