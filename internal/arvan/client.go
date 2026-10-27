// Package arvan talks to ArvanCloud's API: the cloud server a front runs on,
// the public IPs on that server, and the DNS record customers reach it by.
package arvan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Base is the API root; tests point it at an httptest server before New.
var Base = "https://napi.arvancloud.ir"

// ErrNotFound means the server, port or record is not where it was looked for.
// A 404 from the API matches it too.
var ErrNotFound = errors.New("arvan: not found")

// maxBody caps one reply; the largest, a zone's server list, is tens of KB.
const maxBody = 8 << 20

// maxPages stops a paginated listing whose meta never reaches its last page.
const maxPages = 50

// APIError is a non-2xx reply: 402 when the credit is short, 403 for a key
// that lacks the permission.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("arvan: %d: %s", e.Status, e.Message) }

func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

type Client struct {
	key  string
	base string
	hc   *http.Client
}

// New takes the key as the owner pasted it, with or without the "apikey " word
// Arvan's Authorization header needs.
func New(key string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 25 * time.Second}
	}
	return &Client{key: normaliseKey(key), base: strings.TrimRight(Base, "/"), hc: hc}
}

func normaliseKey(key string) string {
	key = strings.TrimSpace(key)
	if f := strings.Fields(key); len(f) == 2 && strings.EqualFold(f[0], "apikey") {
		key = f[1]
	}
	if key == "" {
		return ""
	}
	return "apikey " + key
}

type Zone struct {
	Code    string `json:"code"`
	City    string `json:"city"`
	DC      string `json:"dc"`
	Create  bool   `json:"create"`
	Visible bool   `json:"visible"`
}

type Address struct {
	IP      string
	MAC     string
	Network string
	Version string
	Public  bool
}

type Server struct {
	ID               string
	Name             string
	Status           string
	Zone             string
	Addresses        []Address
	SecurityGroupIDs []string
}

func (s Server) hasIP(ip string) bool {
	for _, a := range s.Addresses {
		if a.IP == ip {
			return true
		}
	}
	return false
}

type Port struct {
	ID               string   `json:"id"`
	NetworkID        string   `json:"network_id"`
	MAC              string   `json:"mac_address"`
	InstanceID       string   `json:"instance_id"`
	Status           string   `json:"status"`
	IPs              []string `json:"ips"`
	SecurityGroupIDs []string `json:"security_group_ids"`
}

type envelope[T any] struct {
	Data T `json:"data"`
	Meta struct {
		CurrentPage int `json:"current_page"`
		LastPage    int `json:"last_page"`
	} `json:"meta"`
}

type wireServer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Addresses map[string][]struct {
		Addr     string          `json:"addr"`
		MAC      string          `json:"mac_addr"`
		Version  json.RawMessage `json:"version"`
		IsPublic bool            `json:"is_public"`
	} `json:"addresses"`
	SecurityGroups []struct {
		ID string `json:"id"`
	} `json:"security_groups"`
}

func (w wireServer) server(zone string) Server {
	s := Server{ID: w.ID, Name: w.Name, Status: w.Status, Zone: zone}
	networks := make([]string, 0, len(w.Addresses))
	for name := range w.Addresses {
		networks = append(networks, name)
	}
	sort.Strings(networks)
	for _, name := range networks {
		for _, a := range w.Addresses[name] {
			s.Addresses = append(s.Addresses, Address{
				IP:      a.Addr,
				MAC:     a.MAC,
				Network: name,
				Version: strings.Trim(string(a.Version), `"`),
				Public:  a.IsPublic,
			})
		}
	}
	for _, sg := range w.SecurityGroups {
		s.SecurityGroupIDs = append(s.SecurityGroupIDs, sg.ID)
	}
	return s
}

func (c *Client) Zones(ctx context.Context) ([]Zone, error) {
	var env envelope[[]Zone]
	if err := c.call(ctx, http.MethodGet, "/ecc/v1/regions", nil, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

func (c *Client) Servers(ctx context.Context, zone string) ([]Server, error) {
	var env envelope[[]wireServer]
	if err := c.call(ctx, http.MethodGet, "/ecc/v1/regions/"+seg(zone)+"/servers", nil, &env); err != nil {
		return nil, err
	}
	out := make([]Server, 0, len(env.Data))
	for _, w := range env.Data {
		out = append(out, w.server(zone))
	}
	return out, nil
}

func (c *Client) Server(ctx context.Context, zone, id string) (Server, error) {
	if id == "" {
		return Server{}, errors.New("arvan: no server id")
	}
	var env envelope[wireServer]
	if err := c.call(ctx, http.MethodGet, "/ecc/v1/regions/"+seg(zone)+"/servers/"+seg(id), nil, &env); err != nil {
		return Server{}, err
	}
	return env.Data.server(zone), nil
}

// FindServerByIP looks through every visible zone. A key limited to one server
// may be refused elsewhere, so a refused zone only matters if the IP is nowhere.
func (c *Client) FindServerByIP(ctx context.Context, ip string) (Server, error) {
	zones, err := c.Zones(ctx)
	if err != nil {
		return Server{}, err
	}
	var refused error
	for _, z := range zones {
		if !z.Visible {
			continue
		}
		servers, err := c.Servers(ctx, z.Code)
		if err != nil {
			if ctx.Err() != nil {
				return Server{}, ctx.Err()
			}
			if refused == nil {
				refused = fmt.Errorf("%w (zone %s: %w)", ErrNotFound, z.Code, err)
			}
			continue
		}
		for _, s := range servers {
			if s.hasIP(ip) {
				return s, nil
			}
		}
	}
	if refused != nil {
		return Server{}, refused
	}
	return Server{}, ErrNotFound
}

func (c *Client) Ports(ctx context.Context, zone string) ([]Port, error) {
	var env envelope[[]Port]
	if err := c.call(ctx, http.MethodGet, "/ecc/v1/regions/"+seg(zone)+"/ports", nil, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

func (c *Client) PortByIP(ctx context.Context, zone, ip string) (Port, error) {
	ports, err := c.Ports(ctx, zone)
	if err != nil {
		return Port{}, err
	}
	for _, p := range ports {
		for _, have := range p.IPs {
			if have == ip {
				return p, nil
			}
		}
	}
	return Port{}, ErrNotFound
}

// AddPublicIP buys one more IPv4 on a new NIC. A reply missing the port or the
// address still returns what it had, so the caller can release a half-made port.
func (c *Client) AddPublicIP(ctx context.Context, zone, serverID string, sgIDs []string) (Port, error) {
	if serverID == "" {
		return Port{}, errors.New("arvan: no server id")
	}
	if sgIDs == nil {
		sgIDs = []string{}
	}
	body := struct {
		Type           string   `json:"type"`
		SecurityGroups []string `json:"security_groups"`
	}{Type: "ipv4", SecurityGroups: sgIDs}
	var env envelope[struct {
		FixedIPs []struct {
			IP string `json:"ip_address"`
		} `json:"fixed_ips"`
		PortID string `json:"port_id"`
		MAC    string `json:"mac_addr"`
	}]
	path := "/ecc/v1/regions/" + seg(zone) + "/servers/" + seg(serverID) + "/add-public-ip"
	if err := c.call(ctx, http.MethodPost, path, body, &env); err != nil {
		return Port{}, err
	}
	p := Port{ID: env.Data.PortID, MAC: env.Data.MAC, InstanceID: serverID}
	for _, f := range env.Data.FixedIPs {
		if f.IP != "" {
			p.IPs = append(p.IPs, f.IP)
		}
	}
	if p.ID == "" || len(p.IPs) == 0 {
		return p, fmt.Errorf("arvan: add-public-ip on %s answered without a port or an address", serverID)
	}
	return p, nil
}

// DeletePort releases the port's IP for good.
func (c *Client) DeletePort(ctx context.Context, zone, portID string) error {
	if portID == "" {
		return errors.New("arvan: no port id")
	}
	return c.call(ctx, http.MethodDelete, "/ecc/v1/regions/"+seg(zone)+"/ports/"+seg(portID), nil, nil)
}

func paginate[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	if query == nil {
		query = url.Values{}
	}
	query.Set("per_page", "100")
	var out []T
	for page := 1; page <= maxPages; page++ {
		query.Set("page", strconv.Itoa(page))
		var env envelope[[]T]
		if err := c.call(ctx, http.MethodGet, path+"?"+query.Encode(), nil, &env); err != nil {
			return nil, err
		}
		out = append(out, env.Data...)
		if len(env.Data) == 0 || env.Meta.LastPage <= page {
			break
		}
	}
	return out, nil
}

func (c *Client) Domains(ctx context.Context) ([]string, error) {
	rows, err := paginate[struct {
		Domain string `json:"domain"`
	}](ctx, c, "/cdn/4.0/domains", nil)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Domain)
	}
	return out, nil
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.key)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("arvan: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("arvan: %s %s: %w", method, path, err)
	}
	if len(raw) > maxBody {
		return fmt.Errorf("arvan: %s %s: reply over %d bytes", method, path, maxBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("arvan: %s %s: unreadable reply: %w", method, path, err)
	}
	return nil
}

func apiError(status int, raw []byte) *APIError {
	var body struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &body)
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &APIError{Status: status, Message: msg}
}

func seg(s string) string { return url.PathEscape(s) }
