package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/arvan"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/ocserv"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
)

// An entry server can be added straight from the ArvanCloud account: the operator lists the
// servers there or has the panel make one. A server the panel makes takes a key the panel
// generated, so it is enrolled with no password through the ordinary add.

// ArvanZoneView is a zone a server can be made in.
type ArvanZoneView struct {
	Code    string `json:"code"`
	City    string `json:"city"`
	DC      string `json:"dc"`
	Country string `json:"country"`
}

// ArvanSizeView is one plan, with Arvan's own prices.
type ArvanSizeView struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	CPUs          int     `json:"cpus"`
	MemoryGB      float64 `json:"memoryGb"`
	DiskGB        int     `json:"diskGb"`
	PricePerHour  float64 `json:"pricePerHour"`
	PricePerMonth float64 `json:"pricePerMonth"`
	Type          string  `json:"type"`
}

// ArvanImageView is one Linux version that takes an SSH key at creation.
type ArvanImageView struct {
	ID           string `json:"id"`
	Distribution string `json:"distribution"`
	Version      string `json:"version"`
}

// ArvanCatalog is what one zone offers a new server.
type ArvanCatalog struct {
	Zones        []ArvanZoneView  `json:"zones"`
	Zone         string           `json:"zone"`
	Sizes        []ArvanSizeView  `json:"sizes"`
	Images       []ArvanImageView `json:"images"`
	ServerCount  int              `json:"serverCount"`
	ServerLimit  int              `json:"serverLimit"`
	DefaultImage string           `json:"defaultImage"`
}

// ArvanServerView is a server on the account; Relay names the entry or exit server of this
// panel at its address, "" when none.
type ArvanServerView struct {
	Zone   string   `json:"zone"`
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	IPs    []string `json:"ips"`
	Relay  string   `json:"relay"`
}

// ArvanCreateRequest is a new entry server to make in Arvan.
type ArvanCreateRequest struct {
	Zone  string `json:"zone"`
	Size  string `json:"size"`
	Image string `json:"image"`
	Name  string `json:"name"`
}

// ArvanCreateState is the running or last make-and-add: its step, and what it has so far.
type ArvanCreateState struct {
	Running    bool   `json:"running"`
	Step       string `json:"step"`
	Error      string `json:"error"`
	Name       string `json:"name"`
	Zone       string `json:"zone"`
	ServerID   string `json:"serverId"`
	IP         string `json:"ip"`
	RelayID    int    `json:"relayId"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt"`
}

// The steps of a make-and-add, in order, as the card names them.
const (
	arvanStepKey    = "key"
	arvanStepServer = "server"
	arvanStepBoot   = "boot"
	arvanStepSSH    = "ssh"
	arvanStepAdd    = "add"
	arvanStepBind   = "bind"
	arvanStepDone   = "done"
)

var arvanCreate struct {
	sync.Mutex
	state ArvanCreateState
}

// Variables so a test need not wait minutes, dial a server or enroll one.
var (
	arvanBootWait   = 10 * time.Minute
	arvanSSHWait    = 5 * time.Minute
	arvanEnrollWait = 20 * time.Minute
	arvanCreatePoll = 5 * time.Second
	arvanSSHReady   = func(ctx context.Context, c relay.Credentials) error {
		cl, err := relay.Dial(ctx, c)
		if err == nil {
			cl.Close()
		}
		return err
	}
	arvanAddFront = func(s *ArvanService, req RelayRequest) (int, error) {
		r, err := s.relays.Add(req)
		if err != nil {
			return 0, err
		}
		return r.Id, nil
	}
	arvanBindFront = func(s *ArvanService, relayId int) error {
		if _, err := s.Detect(relayId); err != nil {
			return err
		}
		return s.Prepare(relayId)
	}
	arvanFrontStatus = func(relayId int) (string, string) {
		var r model.Relay
		if database.GetDB().First(&r, relayId).Error != nil {
			return "", ""
		}
		return r.Status, r.LastError
	}
)

// Catalog lists the zones a server can be made in, and one zone's plans and Linux images;
// zone "" takes Arvan's default zone.
func (s *ArvanService) Catalog(zone string) (ArvanCatalog, error) {
	c, err := s.client()
	if err != nil {
		return ArvanCatalog{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
	defer cancel()
	zones, err := c.Zones(ctx)
	if err != nil {
		return ArvanCatalog{}, err
	}
	out := ArvanCatalog{Zones: []ArvanZoneView{}, Sizes: []ArvanSizeView{}, Images: []ArvanImageView{}}
	for _, z := range zones {
		if z.Visible && z.Create {
			out.Zones = append(out.Zones, ArvanZoneView{Code: z.Code, City: z.City, DC: z.DC, Country: z.Country})
		}
	}
	zone = strings.TrimSpace(zone)
	if zone == "" && len(out.Zones) > 0 {
		zone = out.Zones[0].Code
	}
	if !slices.ContainsFunc(out.Zones, func(z ArvanZoneView) bool { return z.Code == zone }) {
		return out, nil
	}
	out.Zone = zone
	sizes, err := c.Sizes(ctx, zone)
	if err != nil {
		return out, err
	}
	for _, sz := range sizes {
		out.Sizes = append(out.Sizes, ArvanSizeView(sz))
	}
	sort.SliceStable(out.Sizes, func(i, j int) bool { return out.Sizes[i].PricePerHour < out.Sizes[j].PricePerHour })
	images, err := c.Distributions(ctx, zone)
	if err != nil {
		return out, err
	}
	for _, img := range images {
		if !img.SSHKey || !linuxImage(img.Distribution) {
			continue
		}
		out.Images = append(out.Images, ArvanImageView{ID: img.ID, Distribution: img.Distribution, Version: img.Version})
		if out.DefaultImage == "" && strings.EqualFold(img.Distribution, "ubuntu") && img.Version == "24.04" {
			out.DefaultImage = img.ID
		}
	}
	if out.DefaultImage == "" && len(out.Images) > 0 {
		out.DefaultImage = out.Images[0].ID
	}
	if opts, err := c.ServerOptions(ctx, zone); err == nil {
		out.ServerCount, out.ServerLimit = opts.DropletCount, opts.DropletLimit
	}
	return out, nil
}

// linuxImage is a distribution the front's scripts run on.
func linuxImage(distribution string) bool {
	switch strings.ToLower(strings.TrimSpace(distribution)) {
	case "ubuntu", "debian":
		return true
	}
	return false
}

// Servers lists every server on the account, zone by zone, with the relay at its address.
func (s *ArvanService) Servers() ([]ArvanServerView, error) {
	c, err := s.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
	defer cancel()
	zones, err := c.Zones(ctx)
	if err != nil {
		return nil, err
	}
	var relays []model.Relay
	if err := database.GetDB().Find(&relays).Error; err != nil {
		return nil, err
	}
	byAddress := map[string]string{}
	for _, r := range relays {
		byAddress[r.Address] = r.Name
	}
	out := []ArvanServerView{}
	var refused error
	for _, z := range zones {
		if !z.Visible {
			continue
		}
		servers, err := c.Servers(ctx, z.Code)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// a key limited to some zones is refused in the rest; those are only skipped
			refused = err
			continue
		}
		for _, sv := range servers {
			row := ArvanServerView{Zone: z.Code, ID: sv.ID, Name: sv.Name, Status: strings.ToLower(sv.Status), IPs: []string{}}
			for _, a := range sv.Addresses {
				if a.Public && a.IP != "" {
					row.IPs = append(row.IPs, a.IP)
					if name, ok := byAddress[a.IP]; ok && row.Relay == "" {
						row.Relay = name
					}
				}
			}
			out = append(out, row)
		}
	}
	if len(out) == 0 && refused != nil {
		return nil, refused
	}
	return out, nil
}

// CreateState is the running or last make-and-add.
func (s *ArvanService) CreateState() ArvanCreateState {
	arvanCreate.Lock()
	defer arvanCreate.Unlock()
	return arvanCreate.state
}

func setArvanCreate(update func(st *ArvanCreateState)) {
	arvanCreate.Lock()
	defer arvanCreate.Unlock()
	update(&arvanCreate.state)
}

// StartCreate checks the request against the zone's catalog and starts making the server;
// the steps run behind it and CreateState follows them. One runs at a time.
func (s *ArvanService) StartCreate(req ArvanCreateRequest) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	req.Name, req.Zone = strings.TrimSpace(req.Name), strings.TrimSpace(req.Zone)
	if err := ocserv.ValidLocationName(req.Name); err != nil {
		return err
	}
	if len(req.Name) > 40 {
		return fmt.Errorf("the name %q is longer than 40 characters", req.Name)
	}
	var taken int64
	if err := database.GetDB().Model(&model.Relay{}).Where("name = ?", req.Name).Count(&taken).Error; err != nil {
		return err
	}
	if taken > 0 {
		return fmt.Errorf("a relay is already named %q", req.Name)
	}
	cat, err := s.Catalog(req.Zone)
	if err != nil {
		return err
	}
	if cat.Zone == "" || cat.Zone != req.Zone {
		return fmt.Errorf("zone %q does not take new servers on this account", req.Zone)
	}
	i := slices.IndexFunc(cat.Sizes, func(sz ArvanSizeView) bool { return sz.ID == req.Size })
	if i < 0 {
		return fmt.Errorf("zone %s has no size %q", req.Zone, req.Size)
	}
	size := cat.Sizes[i]
	if !slices.ContainsFunc(cat.Images, func(img ArvanImageView) bool { return img.ID == req.Image }) {
		return fmt.Errorf("zone %s has no Linux image %q that takes an SSH key", req.Zone, req.Image)
	}
	if cat.ServerLimit > 0 && cat.ServerCount >= cat.ServerLimit {
		return fmt.Errorf("the account already has %d servers in %s, Arvan's limit", cat.ServerCount, req.Zone)
	}

	arvanCreate.Lock()
	if arvanCreate.state.Running {
		arvanCreate.Unlock()
		return errors.New("a server is already being made; wait for it to finish")
	}
	arvanCreate.state = ArvanCreateState{Running: true, Step: arvanStepKey, Name: req.Name, Zone: req.Zone, StartedAt: time.Now().Unix()}
	arvanCreate.Unlock()

	common.GoRecover("arvan-create", func() { s.runCreate(req, size) })
	return nil
}

func (s *ArvanService) runCreate(req ArvanCreateRequest, size ArvanSizeView) {
	err := s.makeAndAdd(req, size)
	setArvanCreate(func(st *ArvanCreateState) {
		st.Running, st.FinishedAt = false, time.Now().Unix()
		if err != nil {
			st.Error = err.Error()
			return
		}
		st.Step = arvanStepDone
	})
	if err != nil {
		logger.Warningf("arvan: making entry server %s failed: %v", req.Name, err)
	}
}

func (s *ArvanService) makeAndAdd(req ArvanCreateRequest, size ArvanSizeView) error {
	c, err := s.client()
	if err != nil {
		return err
	}
	step := func(name string) { setArvanCreate(func(st *ArvanCreateState) { st.Step = name }) }
	call := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), arvanCallTimeout)
	}

	step(arvanStepKey)
	privatePEM, line, err := relay.GenerateSSHKey("x-ui-arvan")
	if err != nil {
		return err
	}
	keyName := fmt.Sprintf("x-ui-%s-%d", req.Name, time.Now().Unix())
	ctx, cancel := call()
	err = c.AddSSHKey(ctx, req.Zone, keyName, line)
	cancel()
	if err != nil {
		return fmt.Errorf("Arvan refused the panel's SSH key: %w", err)
	}

	step(arvanStepServer)
	ctx, cancel = call()
	opts, err := c.ServerOptions(ctx, req.Zone)
	if err == nil && opts.NetworkID == "" {
		err = errors.New("Arvan named no network for a new server")
	}
	var group arvan.SecurityGroup
	if err == nil {
		var groups []arvan.SecurityGroup
		if groups, err = c.SecurityGroups(ctx, req.Zone); err == nil {
			group, _ = arvan.DefaultSecurityGroup(groups)
		}
	}
	cancel()
	if err != nil {
		return err
	}
	ctx, cancel = call()
	id, err := c.CreateServer(ctx, req.Zone, arvan.NewServer{
		Name: req.Name, SizeID: size.ID, ImageID: req.Image, NetworkID: opts.NetworkID,
		SecurityGroupID: group.ID, KeyName: keyName, DiskGB: size.DiskGB,
	})
	cancel()
	var api *arvan.APIError
	if err != nil && errors.As(err, &api) && api.Status < 500 {
		return fmt.Errorf("Arvan did not make the server: %w", err)
	}
	if err != nil {
		// a reply lost, unreadable or a gateway's 5xx does not mean no server was made, and one made bills
		id = s.findServerByName(c, req.Zone, req.Name)
		if id == "" {
			return fmt.Errorf("Arvan's reply to the create was lost and no server named %s turned up; check the Arvan panel before trying again: %w", req.Name, err)
		}
	}
	setArvanCreate(func(st *ArvanCreateState) { st.ServerID = id })
	logger.Infof("arvan: made server %s (%s) in %s for an entry server", req.Name, id, req.Zone)

	step(arvanStepBoot)
	ip, err := s.waitServerUp(c, req.Zone, id)
	if err != nil {
		return fmt.Errorf("server %s was made but %w; it is billed until deleted in the Arvan panel", req.Name, err)
	}
	setArvanCreate(func(st *ArvanCreateState) { st.IP = ip })

	step(arvanStepSSH)
	creds := relay.Credentials{Address: ip, Port: 22, User: "root", PrivateKey: privatePEM}
	if err := waitFor(arvanSSHWait, func(ctx context.Context) error { return arvanSSHReady(ctx, creds) }); err != nil {
		return fmt.Errorf("server %s at %s is up but did not take the panel's key over SSH: %w", req.Name, ip, err)
	}

	step(arvanStepAdd)
	relayId, err := arvanAddFront(s, RelayRequest{
		Name: req.Name, Address: ip, SSHPort: 22, SSHUser: "root", PrivateKey: string(privatePEM),
		Role: string(model.RelayFront), Tool: "backpack", Transport: "stealth", TunnelPort: 3080,
	})
	if err != nil {
		return fmt.Errorf("server %s at %s is ready but could not be added as an entry server: %w", req.Name, ip, err)
	}
	setArvanCreate(func(st *ArvanCreateState) { st.RelayID = relayId })

	step(arvanStepBind)
	deadline := time.Now().Add(arvanEnrollWait)
	for {
		status, lastErr := arvanFrontStatus(relayId)
		if status == RelayReady {
			break
		}
		if status == RelayError || status == "" {
			return fmt.Errorf("entry server %s was added but setting it up failed: %s", req.Name, lastErr)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("entry server %s is still being set up; open it in a few minutes", req.Name)
		}
		time.Sleep(arvanCreatePoll)
	}
	if err := arvanBindFront(s, relayId); err != nil {
		return fmt.Errorf("entry server %s works, but binding it to Arvan for IP swaps failed; press Detect in its Arvan window: %w", req.Name, err)
	}
	return nil
}

// findServerByName is the id of the zone's server named name, looked for a few times.
func (s *ArvanService) findServerByName(c *arvan.Client, zone, name string) string {
	for range 6 {
		ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
		servers, err := c.Servers(ctx, zone)
		cancel()
		if err == nil {
			for _, sv := range servers {
				if sv.Name == name {
					return sv.ID
				}
			}
		}
		time.Sleep(arvanCreatePoll)
	}
	return ""
}

// waitServerUp waits for the server to be active with a public IPv4, which it returns.
func (s *ArvanService) waitServerUp(c *arvan.Client, zone, id string) (string, error) {
	var ip string
	err := waitFor(arvanBootWait, func(ctx context.Context) error {
		sv, err := c.Server(ctx, zone, id)
		if err != nil {
			return err
		}
		switch status := strings.ToLower(sv.Status); status {
		case "active":
			if ip = sv.PublicIPv4(); ip == "" || net.ParseIP(ip) == nil {
				return errors.New("it is active but has no public IPv4 yet")
			}
			return nil
		case "error":
			return errStop{fmt.Errorf("Arvan reports it in error")}
		default:
			return fmt.Errorf("it is still %s", status)
		}
	})
	return ip, err
}

// errStop ends a waitFor at once.
type errStop struct{ error }

func (e errStop) Unwrap() error { return e.error }

// waitFor retries try every arvanCreatePoll until it succeeds or within passes; the last
// error is returned, and an errStop ends the wait at once.
func waitFor(within time.Duration, try func(ctx context.Context) error) error {
	deadline := time.Now().Add(within)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
		err := try(ctx)
		cancel()
		var stop errStop
		if err == nil || errors.As(err, &stop) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gave up after %s: %w", within, err)
		}
		time.Sleep(arvanCreatePoll)
	}
}

// DeleteServer removes a server from the Arvan account for good. One whose public IP an entry
// or exit server of this panel uses is refused: removing it here first keeps that relay from
// pointing at a machine that is gone. So is the one a make-and-add is working on.
func (s *ArvanService) DeleteServer(zone, id string) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	zone, id = strings.TrimSpace(zone), strings.TrimSpace(id)
	if zone == "" || id == "" {
		return errors.New("which server should be deleted?")
	}
	if st := s.CreateState(); st.Running && st.ServerID == id {
		return errors.New("this server is still being made; wait for it to finish")
	}
	c, err := s.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
	defer cancel()
	server, err := c.Server(ctx, zone, id)
	if err != nil {
		return err
	}
	for _, a := range server.Addresses {
		if a.IP == "" {
			continue
		}
		var r model.Relay
		if database.GetDB().Where("address = ?", a.IP).First(&r).Error == nil {
			return fmt.Errorf("%s (%s) is the address of %s on this panel; remove that server here first", server.Name, a.IP, r.Name)
		}
	}
	if err := c.DeleteServer(ctx, zone, id); err != nil {
		return err
	}
	logger.Infof("arvan: deleted server %s (%s) in %s", server.Name, id, zone)
	return nil
}
