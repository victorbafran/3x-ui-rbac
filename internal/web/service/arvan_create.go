package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
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
	"github.com/mhsanaei/3x-ui/v3/internal/tunnel"
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

// ArvanPurpose is what a server from Arvan becomes here: an entry server ("front", the
// default) carried by Tool and Transport, whose public IP goes behind Record.Domain when
// Domain is set, or an exit ("exit"), which only a zone outside Iran makes sense for.
type ArvanPurpose struct {
	Role      string `json:"role"`
	Tool      string `json:"tool"`
	Transport string `json:"transport"`
	Domain    string `json:"domain"`
	Record    string `json:"record"`
}

// ArvanCreateRequest is a new server to make in Arvan and add here.
type ArvanCreateRequest struct {
	Zone  string `json:"zone"`
	Size  string `json:"size"`
	Image string `json:"image"`
	Name  string `json:"name"`
	ArvanPurpose
}

// ArvanAdoptRequest adds a server already on the account; its root password is used once.
type ArvanAdoptRequest struct {
	Zone     string `json:"zone"`
	ServerID string `json:"serverId"`
	Name     string `json:"name"`
	Password string `json:"password"`
	ArvanPurpose
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
	// Steps are this run's steps in order: a make runs them all, adding a server that is
	// already there starts at add, and an exit has no dns or bind.
	Steps []string `json:"steps"`
}

// The steps of a make-and-add, in order, as the card names them.
const (
	arvanStepKey    = "key"
	arvanStepServer = "server"
	arvanStepBoot   = "boot"
	arvanStepSSH    = "ssh"
	arvanStepAdd    = "add"
	arvanStepSetup  = "setup"
	arvanStepDNS    = "dns"
	arvanStepBind   = "bind"
	arvanStepDone   = "done"
)

// arvanSteps is the steps of a run that makes the server or adopts one, for a purpose.
func arvanSteps(makes bool, p ArvanPurpose) []string {
	var steps []string
	if makes {
		steps = []string{arvanStepKey, arvanStepServer, arvanStepBoot, arvanStepSSH}
	}
	steps = append(steps, arvanStepAdd, arvanStepSetup)
	if p.Role == string(model.RelayFront) {
		if p.Domain != "" {
			steps = append(steps, arvanStepDNS)
		}
		steps = append(steps, arvanStepBind)
	}
	return steps
}

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
	// Every zone is asked at once: one at a time, a round trip each, the list took seconds.
	// Results keep the zones' order.
	type zoneServers struct {
		servers []arvan.Server
		err     error
	}
	got := make([]zoneServers, len(zones))
	var wg sync.WaitGroup
	for i, z := range zones {
		if !z.Visible {
			continue
		}
		wg.Go(func() {
			got[i].servers, got[i].err = c.Servers(ctx, z.Code)
		})
	}
	wg.Wait()
	out := []ArvanServerView{}
	var refused error
	for i, z := range zones {
		if !z.Visible {
			continue
		}
		servers, err := got[i].servers, got[i].err
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

// arvanRecordName is a record label as it is typed in Arvan: "@" for the domain itself, or
// labels of letters, digits and hyphens joined by dots.
var arvanRecordLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validArvanRecord(name string) bool {
	if name == "@" {
		return true
	}
	if name == "" || len(name) > 200 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !arvanRecordLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// normalizePurpose checks a purpose against what the zone is and the domains on the account,
// filling the defaults: an entry server on BackPack stealth.
func (s *ArvanService) normalizePurpose(c *arvan.Client, p ArvanPurpose, zoneCountry string) (ArvanPurpose, error) {
	p.Role = strings.TrimSpace(p.Role)
	if p.Role == "" {
		p.Role = string(model.RelayFront)
	}
	p.Domain = strings.ToLower(strings.TrimSpace(p.Domain))
	p.Record = strings.ToLower(strings.TrimSpace(p.Record))
	switch model.RelayRole(p.Role) {
	case model.RelayExit:
		// customers leaving through an exit come out in its country
		if strings.EqualFold(strings.TrimSpace(zoneCountry), "iran") {
			return p, errors.New("an exit in Iran would send customers out through Iran; pick a zone outside Iran or make it an entry server")
		}
		return ArvanPurpose{Role: p.Role}, nil
	case model.RelayFront:
	default:
		return p, fmt.Errorf("a server is an entry server or an exit, not %q", p.Role)
	}
	if strings.TrimSpace(p.Tool) == "" {
		p.Tool = string(tunnel.ToolBackPack)
	}
	tool, err := tunnel.ParseTool(strings.TrimSpace(p.Tool))
	if err != nil {
		return p, err
	}
	p.Tool = string(tool)
	if strings.TrimSpace(p.Transport) == "" {
		p.Transport = string(tunnel.TransportsFor(tool)[0])
	}
	p.Transport = strings.TrimSpace(p.Transport)
	if err := tunnel.ValidChoiceFor(tool, tunnel.Transport(p.Transport)); err != nil {
		return p, err
	}
	if p.Domain == "" {
		p.Record = ""
		return p, nil
	}
	if !validArvanRecord(p.Record) {
		return p, fmt.Errorf("%q is not a record name: use letters, digits and hyphens, or @ for the domain itself", p.Record)
	}
	ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
	defer cancel()
	domains, err := c.Domains(ctx)
	if err != nil {
		return p, err
	}
	if !slices.Contains(domains, p.Domain) {
		return p, fmt.Errorf("the domain %s is not on this Arvan account", p.Domain)
	}
	return p, nil
}

// Domains lists the domains on the Arvan account, for a server's IP to go behind.
func (s *ArvanService) Domains() ([]string, error) {
	c, err := s.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
	defer cancel()
	domains, err := c.Domains(ctx)
	if err != nil {
		return nil, err
	}
	slices.Sort(domains)
	return domains, nil
}

// relayNameFree refuses a name a relay here already has.
func relayNameFree(name string) error {
	if err := ocserv.ValidLocationName(name); err != nil {
		return err
	}
	if len(name) > 40 {
		return fmt.Errorf("the name %q is longer than 40 characters", name)
	}
	var taken int64
	if err := database.GetDB().Model(&model.Relay{}).Where("name = ?", name).Count(&taken).Error; err != nil {
		return err
	}
	if taken > 0 {
		return fmt.Errorf("a relay is already named %q", name)
	}
	return nil
}

// claimArvanRun starts a run's state, or refuses while one runs.
func claimArvanRun(name, zone string, steps []string) error {
	arvanCreate.Lock()
	defer arvanCreate.Unlock()
	if arvanCreate.state.Running {
		return errors.New("a server is already being made or added; wait for it to finish")
	}
	arvanCreate.state = ArvanCreateState{Running: true, Step: steps[0], Name: name, Zone: zone, StartedAt: time.Now().Unix(), Steps: steps}
	return nil
}

// StartCreate checks the request against the zone's catalog and starts making the server;
// the steps run behind it and CreateState follows them. One runs at a time.
func (s *ArvanService) StartCreate(req ArvanCreateRequest) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	req.Name, req.Zone = strings.TrimSpace(req.Name), strings.TrimSpace(req.Zone)
	if err := relayNameFree(req.Name); err != nil {
		return err
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
	c, err := s.client()
	if err != nil {
		return err
	}
	zi := slices.IndexFunc(cat.Zones, func(z ArvanZoneView) bool { return z.Code == req.Zone })
	if req.ArvanPurpose, err = s.normalizePurpose(c, req.ArvanPurpose, cat.Zones[zi].Country); err != nil {
		return err
	}
	if err := claimArvanRun(req.Name, req.Zone, arvanSteps(true, req.ArvanPurpose)); err != nil {
		return err
	}

	common.GoRecover("arvan-create", func() { s.runCreate(req, size) })
	return nil
}

func (s *ArvanService) runCreate(req ArvanCreateRequest, size ArvanSizeView) {
	finishArvanRun(req.Name, s.makeAndAdd(req, size))
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
	// Arvan caps the keys an account holds, and every make used to leave one behind.
	arvanSweepKeys(c, "")
	ctx, cancel := call()
	err = c.AddSSHKey(ctx, req.Zone, keyName, line)
	cancel()
	if err != nil {
		return fmt.Errorf("Arvan refused the panel's SSH key: %w", err)
	}
	// The key matters only while the server is made and first boots; it goes when this run ends.
	defer func() {
		ctx, cancel := call()
		defer cancel()
		if derr := c.DeleteSSHKey(ctx, req.Zone, keyName); derr != nil {
			logger.Warningf("arvan: could not remove the one-time SSH key %s: %v", keyName, derr)
		}
	}()

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

	return s.addAndFinish(c, req.Name, ip, req.ArvanPurpose, RelayRequest{
		Name: req.Name, Address: ip, SSHPort: 22, SSHUser: "root", PrivateKey: string(privatePEM),
	})
}

// addAndFinish adds the server here for its purpose, waits for it to be set up, puts its IP
// behind the record when a domain was chosen, and binds an entry server to Arvan for swaps.
func (s *ArvanService) addAndFinish(c *arvan.Client, name, ip string, p ArvanPurpose, req RelayRequest) error {
	step := func(n string) { setArvanCreate(func(st *ArvanCreateState) { st.Step = n }) }
	step(arvanStepAdd)
	req.Role = p.Role
	if p.Role == string(model.RelayFront) {
		req.Tool, req.Transport, req.TunnelPort = p.Tool, p.Transport, 3080
	}
	relayId, err := arvanAddFront(s, req)
	if err != nil {
		return fmt.Errorf("server %s at %s could not be added here: %w", name, ip, err)
	}
	setArvanCreate(func(st *ArvanCreateState) { st.RelayID = relayId })

	step(arvanStepSetup)
	deadline := time.Now().Add(arvanEnrollWait)
	for {
		status, lastErr := arvanFrontStatus(relayId)
		if status == RelayReady {
			break
		}
		if status == RelayError || status == "" {
			return fmt.Errorf("%s was added but setting it up failed: %s", name, lastErr)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s is still being set up; open it in a few minutes", name)
		}
		time.Sleep(arvanCreatePoll)
	}
	if p.Role != string(model.RelayFront) {
		return nil
	}
	var dnsErr error
	if p.Domain != "" {
		step(arvanStepDNS)
		if dnsErr = arvanPointRecord(c, relayId, p.Domain, p.Record, ip); dnsErr == nil {
			// the record is the name customers reach it by, and the one a swap moves
			_ = database.GetDB().Model(&model.Relay{}).Where("id = ?", relayId).Update("public_name", recordFQDN(p.Record, p.Domain)).Error
		}
	}
	step(arvanStepBind)
	bindErr := arvanBindFront(s, relayId)
	switch {
	case dnsErr != nil && bindErr != nil:
		return fmt.Errorf("entry server %s works, but %s could not be pointed at it (%w) and binding it to Arvan failed (%w)", name, recordFQDN(p.Record, p.Domain), dnsErr, bindErr)
	case dnsErr != nil:
		return fmt.Errorf("entry server %s works and is bound for swaps, but %s could not be pointed at it: %w", name, recordFQDN(p.Record, p.Domain), dnsErr)
	case bindErr != nil:
		return fmt.Errorf("entry server %s works, but binding it to Arvan for IP swaps failed; press Detect in its Arvan window: %w", name, bindErr)
	}
	return nil
}

// arvanPointRecord puts ip behind domain's record name: the record is made when there is none,
// and moved when there is, unless it points at the address of another relay here, which a move
// would cut off.
var arvanPointRecord = func(c *arvan.Client, relayId int, domain, name, ip string) error {
	ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
	defer cancel()
	recs, err := c.ARecords(ctx, domain)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if !strings.EqualFold(rec.Name, name) {
			continue
		}
		for _, held := range rec.IPs {
			if held == ip {
				return nil
			}
			var other model.Relay
			if database.GetDB().Where("address = ? AND id <> ?", held, relayId).First(&other).Error == nil {
				return fmt.Errorf("%s points at %s, the address of %s here; pick another name", recordFQDN(name, domain), held, other.Name)
			}
		}
		return c.SetARecordIP(ctx, domain, rec, ip)
	}
	return c.CreateARecord(ctx, domain, name, ip)
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

// StartAdopt adds a server already on the account, by its root password once, for a purpose;
// the steps run behind it and CreateState follows them.
func (s *ArvanService) StartAdopt(req ArvanAdoptRequest) error {
	if global.IsStandby() {
		return global.ErrStandby
	}
	req.Name, req.Zone, req.ServerID = strings.TrimSpace(req.Name), strings.TrimSpace(req.Zone), strings.TrimSpace(req.ServerID)
	if err := relayNameFree(req.Name); err != nil {
		return err
	}
	if req.Password == "" {
		return errors.New("the server's root password is needed once, to install the panel's own key")
	}
	c, err := s.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
	defer cancel()
	zones, err := c.Zones(ctx)
	if err != nil {
		return err
	}
	zi := slices.IndexFunc(zones, func(z arvan.Zone) bool { return z.Code == req.Zone })
	if zi < 0 {
		return fmt.Errorf("zone %q is not on this account", req.Zone)
	}
	server, err := c.Server(ctx, req.Zone, req.ServerID)
	if err != nil {
		return err
	}
	ip := server.PublicIPv4()
	if ip == "" {
		return fmt.Errorf("%s has no public IPv4 to reach it by", server.Name)
	}
	var held model.Relay
	if database.GetDB().Where("address = ?", ip).First(&held).Error == nil {
		return fmt.Errorf("%s (%s) is already %s on this panel", server.Name, ip, held.Name)
	}
	if req.ArvanPurpose, err = s.normalizePurpose(c, req.ArvanPurpose, zones[zi].Country); err != nil {
		return err
	}
	if err := claimArvanRun(req.Name, req.Zone, arvanSteps(false, req.ArvanPurpose)); err != nil {
		return err
	}
	setArvanCreate(func(st *ArvanCreateState) { st.ServerID, st.IP = req.ServerID, ip })
	common.GoRecover("arvan-adopt", func() {
		err := s.addAndFinish(c, req.Name, ip, req.ArvanPurpose, RelayRequest{
			Name: req.Name, Address: ip, SSHPort: 22, SSHUser: "root", Password: req.Password,
		})
		finishArvanRun(req.Name, err)
	})
	return nil
}

// finishArvanRun records how a run ended.
func finishArvanRun(name string, err error) {
	setArvanCreate(func(st *ArvanCreateState) {
		st.Running, st.FinishedAt = false, time.Now().Unix()
		if err != nil {
			st.Error = err.Error()
			return
		}
		st.Step = arvanStepDone
	})
	if err != nil {
		logger.Warningf("arvan: %s: %v", name, err)
	}
}

// arvanPanelKey is the name a make gives the one-time key it registers: x-ui-<name>-<unix time>.
var arvanPanelKey = regexp.MustCompile(`^x-ui-.+-\d{10}$`)

// arvanSweepKeys removes the one-time keys earlier makes left in every zone but the one named
// keep; a key anybody else registered is never touched. Best effort: a zone it cannot read is
// skipped.
func arvanSweepKeys(c *arvan.Client, keep string) {
	ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
	defer cancel()
	zones, err := c.Zones(ctx)
	if err != nil {
		return
	}
	for _, z := range zones {
		if !z.Visible {
			continue
		}
		names, err := c.SSHKeyNames(ctx, z.Code)
		if err != nil {
			continue
		}
		for _, name := range names {
			if name == keep || !arvanPanelKey.MatchString(name) {
				continue
			}
			if err := c.DeleteSSHKey(ctx, z.Code, name); err != nil {
				logger.Warningf("arvan: could not remove the leftover SSH key %s in %s: %v", name, z.Code, err)
			}
		}
	}
}

// PointRecord puts a front's public IP behind domain's record name, made or moved as a new
// server's is, and binds that record to the front: it becomes the name customers reach it by
// and the one a swap moves. The record the front had before is left where it points.
func (s *ArvanService) PointRecord(relayId int, domain, record string) (ArvanFrontView, error) {
	if global.IsStandby() {
		return ArvanFrontView{}, global.ErrStandby
	}
	r, err := arvanRelay(relayId)
	if err != nil {
		return ArvanFrontView{}, err
	}
	if net.ParseIP(r.Address) == nil || net.ParseIP(r.Address).To4() == nil {
		return ArvanFrontView{}, fmt.Errorf("%s is not an IPv4 address; a record can only point at the front's IP", r.Address)
	}
	c, err := s.client()
	if err != nil {
		return ArvanFrontView{}, err
	}
	p, err := s.normalizePurpose(c, ArvanPurpose{Role: string(model.RelayFront), Tool: string(tunnel.ToolBackPack), Domain: domain, Record: record}, "")
	if err != nil {
		return ArvanFrontView{}, err
	}
	if p.Domain == "" {
		return ArvanFrontView{}, errors.New("pick a domain")
	}
	st := arvanStateOf(relayId)
	if busy, ok := st.claim("record"); !ok {
		return ArvanFrontView{}, errArvanBusy(busy)
	}
	err = func() error {
		defer st.done()
		st.lock.Lock()
		defer st.lock.Unlock()
		if err := arvanPointRecord(c, relayId, p.Domain, p.Record, r.Address); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), arvanCallTimeout)
		defer cancel()
		recs, err := c.ARecords(ctx, p.Domain)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(recs, func(rec arvan.Record) bool { return strings.EqualFold(rec.Name, p.Record) })
		if i < 0 {
			return fmt.Errorf("%s was written but Arvan does not list it yet; press Find on ArvanCloud in a minute", recordFQDN(p.Record, p.Domain))
		}
		if err := ensureArvanFront(relayId); err != nil {
			return err
		}
		fqdn := recordFQDN(p.Record, p.Domain)
		if err := database.GetDB().Model(&model.Relay{}).Where("id = ?", relayId).Update("public_name", fqdn).Error; err != nil {
			return err
		}
		st.setRecordIP(r.Address)
		return updateArvanFront(relayId, map[string]any{
			"domain": p.Domain, "record_name": recs[i].Name, "record_id": recs[i].ID,
			"last_action": "record: " + fqdn + " → " + r.Address, "last_error": "",
		})
	}()
	if err != nil {
		return ArvanFrontView{}, err
	}
	return s.View(relayId)
}
