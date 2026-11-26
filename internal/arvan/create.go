package arvan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// The calls below make a new cloud server. Their shapes follow ArvanCloud's own Terraform
// provider (internal/api/iaas/{server,sizes,image,securitygroup,sshkey}.go) and the live
// replies read on 2026-11-26.

// Size is one plan a server can be made on. Prices are as Arvan's list gives them.
type Size struct {
	ID            string
	Name          string
	CPUs          int
	MemoryGB      float64
	DiskGB        int
	PricePerHour  float64
	PricePerMonth float64
	Type          string
}

// flexNumber reads a number Arvan sends either bare or as a string.
type flexNumber float64

func (f *flexNumber) UnmarshalJSON(raw []byte) error {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("arvan: %q is not a number", s)
	}
	*f = flexNumber(v)
	return nil
}

type wireSize struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	CPUCount      flexNumber `json:"cpu_count"`
	Memory        flexNumber `json:"memory"`
	Disk          flexNumber `json:"disk"`
	PricePerHour  flexNumber `json:"price_per_hour"`
	PricePerMonth flexNumber `json:"price_per_month"`
	Type          string     `json:"type"`
}

// Sizes lists the plans of a zone.
func (c *Client) Sizes(ctx context.Context, zone string) ([]Size, error) {
	var env envelope[[]wireSize]
	if err := c.call(ctx, http.MethodGet, "/ecc/v1/regions/"+seg(zone)+"/sizes", nil, &env); err != nil {
		return nil, err
	}
	out := make([]Size, 0, len(env.Data))
	for _, w := range env.Data {
		out = append(out, Size{
			ID: w.ID, Name: w.Name, CPUs: int(w.CPUCount), MemoryGB: float64(w.Memory), DiskGB: int(w.Disk),
			PricePerHour: float64(w.PricePerHour), PricePerMonth: float64(w.PricePerMonth), Type: w.Type,
		})
	}
	return out, nil
}

// Image is one operating system version a server can start from.
type Image struct {
	ID           string `json:"id"`
	Version      string `json:"name"`
	Distribution string `json:"distribution_name"`
	// SSHKey is whether a server made from it takes an SSH key at creation.
	SSHKey bool `json:"ssh_key"`
}

// Distributions lists the operating systems of a zone, each with its versions.
func (c *Client) Distributions(ctx context.Context, zone string) ([]Image, error) {
	var env envelope[[]struct {
		Name   string  `json:"name"`
		Images []Image `json:"images"`
	}]
	if err := c.call(ctx, http.MethodGet, "/ecc/v1/regions/"+seg(zone)+"/images?type=distributions", nil, &env); err != nil {
		return nil, err
	}
	var out []Image
	for _, d := range env.Data {
		for _, img := range d.Images {
			if img.Distribution == "" {
				img.Distribution = d.Name
			}
			out = append(out, img)
		}
	}
	return out, nil
}

// ServerOptions is what a zone offers a new server: the network it gets by default and how
// many servers the account may still make there.
type ServerOptions struct {
	NetworkID    string `json:"network_id"`
	DropletCount int    `json:"droplet_count"`
	DropletLimit int    `json:"droplet_limit"`
}

func (c *Client) ServerOptions(ctx context.Context, zone string) (ServerOptions, error) {
	var env envelope[ServerOptions]
	if err := c.call(ctx, http.MethodGet, "/ecc/v1/regions/"+seg(zone)+"/servers/options", nil, &env); err != nil {
		return ServerOptions{}, err
	}
	return env.Data, nil
}

// SecurityGroup is a zone's firewall group.
type SecurityGroup struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
	Default  bool   `json:"default"`
}

func (c *Client) SecurityGroups(ctx context.Context, zone string) ([]SecurityGroup, error) {
	var env envelope[[]SecurityGroup]
	if err := c.call(ctx, http.MethodGet, "/ecc/v1/regions/"+seg(zone)+"/securities", nil, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

// DefaultSecurityGroup is the group every server gets unless told otherwise: flagged default,
// or named arDefault as the Terraform provider finds it.
func DefaultSecurityGroup(groups []SecurityGroup) (SecurityGroup, bool) {
	for _, g := range groups {
		if g.Default {
			return g, true
		}
	}
	for _, g := range groups {
		if g.RealName == "arDefault" || strings.HasSuffix(g.RealName, "_arDefault") {
			return g, true
		}
	}
	return SecurityGroup{}, false
}

// AddSSHKey registers a public key in a zone under name, for a new server to be made with.
func (c *Client) AddSSHKey(ctx context.Context, zone, name, publicKey string) error {
	body := struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}{Name: name, PublicKey: publicKey}
	return c.call(ctx, http.MethodPost, "/ecc/v1/regions/"+seg(zone)+"/ssh-keys", body, nil)
}

// NewServer is what a server is made with.
type NewServer struct {
	Name            string
	SizeID          string
	ImageID         string
	NetworkID       string
	SecurityGroupID string
	// KeyName is a key AddSSHKey registered; the server takes it for root at first boot.
	KeyName string
	DiskGB  int
}

// ErrNoServerID is a create Arvan accepted but whose reply named no server.
var ErrNoServerID = errors.New("arvan: the server was created but the reply named no id")

// CreateServer makes one server and returns its id. Arvan bills it from now on.
func (c *Client) CreateServer(ctx context.Context, zone string, s NewServer) (string, error) {
	if s.Name == "" || s.SizeID == "" || s.ImageID == "" || s.NetworkID == "" {
		return "", errors.New("arvan: a server needs a name, a size, an image and a network")
	}
	type group struct {
		Name string `json:"name"`
	}
	body := struct {
		Name           string   `json:"name"`
		NetworkIDs     []string `json:"network_ids"`
		FlavorID       string   `json:"flavor_id"`
		ImageID        string   `json:"image_id"`
		SecurityGroups []group  `json:"security_groups"`
		SSHKey         bool     `json:"ssh_key"`
		KeyName        any      `json:"key_name"`
		Count          int      `json:"count"`
		DiskSize       int      `json:"disk_size,omitempty"`
		HAEnabled      bool     `json:"ha_enabled"`
	}{
		Name: s.Name, NetworkIDs: []string{s.NetworkID}, FlavorID: s.SizeID, ImageID: s.ImageID,
		SecurityGroups: []group{}, SSHKey: s.KeyName != "", KeyName: 0, Count: 1, DiskSize: s.DiskGB,
	}
	// The provider sends the group's id in the field it calls name.
	if s.SecurityGroupID != "" {
		body.SecurityGroups = append(body.SecurityGroups, group{Name: s.SecurityGroupID})
	}
	if s.KeyName != "" {
		body.KeyName = s.KeyName
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := c.call(ctx, http.MethodPost, "/ecc/v1/regions/"+seg(zone)+"/servers", body, &env); err != nil {
		return "", err
	}
	var one struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(env.Data, &one) == nil && one.ID != "" {
		return one.ID, nil
	}
	var many []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(env.Data, &many) == nil && len(many) > 0 && many[0].ID != "" {
		return many[0].ID, nil
	}
	return "", ErrNoServerID
}

// PublicIPv4 is the server's first public IPv4, "" while it has none.
func (s Server) PublicIPv4() string {
	for _, a := range s.Addresses {
		if a.Public && a.Version == "4" && a.IP != "" && !strings.Contains(a.IP, ":") {
			return a.IP
		}
	}
	return ""
}

// DeleteServer removes a server for good, with its disk and IPs; Arvan stops billing it.
func (c *Client) DeleteServer(ctx context.Context, zone, id string) error {
	if id == "" {
		return errors.New("arvan: no server id")
	}
	return c.call(ctx, http.MethodDelete, "/ecc/v1/regions/"+seg(zone)+"/servers/"+seg(id)+"?forceDelete=true", nil, nil)
}
