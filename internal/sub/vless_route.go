package sub

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// xray reads the route from UUID bytes 6-7 (net.PortFromBytes) and masks them to
// zero before auth, so baking a 0-65535 value into the 3rd group routes without
// breaking the user match. Empty/invalid/non-UUID input is returned unchanged.
func applyVlessRoute(id, route string) string {
	n, ok := routeValue(route)
	if !ok {
		return id
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return id
	}
	u[6] = byte(n >> 8)
	u[7] = byte(n)
	return u.String()
}

func routeValue(route string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(route))
	return n, err == nil && n >= 0 && n <= 65535
}

// hostHysteriaAuth writes an exit host's route into the auth as Xray's Hysteria
// inbound reads it, bytes 6-7, keeping the rest as spelt; other hosts keep it whole.
func hostHysteriaAuth(auth string, ep map[string]any) string {
	if exit, _ := ep["exit"].(string); exit == "" {
		return auth
	}
	n, ok := routeValue(hostVlessRoute(ep))
	if !ok || !service.HysteriaAuthCarriesRoute(auth) {
		return auth
	}
	at := 12
	if len(auth) == 36 {
		at = 14
	}
	return auth[:at] + fmt.Sprintf("%04x", n) + auth[at+4:]
}

func hostVlessRoute(ep map[string]any) string {
	v, _ := ep["vlessRoute"].(string)
	return v
}
