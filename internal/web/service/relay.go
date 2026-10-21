package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/crypto/nodetoken"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/ocserv"
	"github.com/mhsanaei/3x-ui/v3/internal/relay"
	"github.com/mhsanaei/3x-ui/v3/internal/tunnel"
)

// A relay is a plain server in another country that a cisco inbound's traffic
// can leave through. The panel installs WireGuard on it over SSH and nothing
// else: no panel, no xray, no account. What the operator gets back is one more
// country in the dropdown their customers already see.

// Relay status values, as stored and as the UI reads them.
const (
	RelayPending  = "pending"
	RelayWorking  = "working"
	RelayReady    = "ready"
	RelayError    = "error"
	RelayDisabled = "disabled"
)

// relayKeyDir is where the panel keeps the SSH keys it installed. /etc/x-ui is
// one of the few paths the panel's own systemd unit may write to, and it is
// the directory a backup already covers.
func relayKeyDir() string { return "/etc/x-ui/relay-keys" }

// provisionTimeout has to cover apt-get on a slow link from a server that may
// still be booting.
const provisionTimeout = 8 * time.Minute

type RelayService struct{}

// running guards against two provisions of the same relay at once, which an
// operator produces simply by pressing save twice.
var relayRunning sync.Map

// RelayView is one relay as the panel shows it: the row, plus what the tunnel
// is actually doing. Secrets never appear.
type RelayView struct {
	model.Relay
	Interface    string `json:"interface"`
	TunnelUp     bool   `json:"tunnelUp"`
	Healthy      bool   `json:"healthy"`
	HandshakeAgo int64  `json:"handshakeAgo"`
	Received     int64  `json:"received"`
	Sent         int64  `json:"sent"`
	// InUseBy names the inbounds that offer this relay as a country, which is
	// what makes deleting it a question rather than a button.
	InUseBy []string `json:"inUseBy"`
	// A front's own: what its client last said when it is not running, and
	// the customer ports it carries right now, derived the same way
	// provisioning derives them so the page shows what the front really opens.
	TunnelDetail   string `json:"tunnelDetail"`
	ForwardedPorts []int  `json:"forwardedPorts"`
}

// RelayRequest is what the operator fills in. Password and PrivateKey are
// write-only: they are used to enroll and never stored or returned.
type RelayRequest struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Address    string `json:"address"`
	SSHPort    int    `json:"sshPort"`
	SSHUser    string `json:"sshUser"`
	Password   string `json:"password"`
	PrivateKey string `json:"privateKey"`
	Enabled    *bool  `json:"enabled"`

	// Role is "exit" (the default, and what every relay was before this) or
	// "front". A blank role is an exit, so a client that predates the field
	// keeps enrolling exits.
	Role string `json:"role"`
	// TunnelPort and Transport only mean anything for a front. They are
	// ignored on an exit rather than rejected, so switching a relay's role
	// does not require clearing fields that were never relevant.
	TunnelPort int    `json:"tunnelPort"`
	Transport  string `json:"transport"`
	// Tool is "backhaul" (the default, and what every front was before the
	// choice) or "backpack". A front's tool is fixed once it is enrolled.
	Tool string `json:"tool"`
}

// List returns every relay with the state of its tunnel.
func (s *RelayService) List() ([]RelayView, error) {
	var rows []model.Relay
	if err := database.GetDB().Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	usage := s.usage()
	out := make([]RelayView, 0, len(rows))
	for _, r := range rows {
		view := RelayView{
			Relay: r,
			// A miss in the map is a nil slice, and a nil slice marshals to
			// null rather than []. The page reads .length off this and a null
			// took the whole page down, so the empty case is made explicit
			// here rather than left to whoever reads the JSON next.
			InUseBy:        inUse(usage, relay.InterfaceName(r.Id)),
			ForwardedPorts: []int{},
		}
		if r.Role == model.RelayFront {
			s.frontHealth(&view)
		} else {
			h := relay.Check(relay.Server{Id: r.Id})
			view.Interface = relay.InterfaceName(r.Id)
			view.TunnelUp = h.Up
			view.Healthy = h.Healthy()
			view.HandshakeAgo = int64(h.HandshakeAge.Seconds())
			view.Received = h.Received
			view.Sent = h.Sent
		}
		out = append(out, view)
	}
	return out, nil
}

// frontHealth fills in what a front's tunnel is doing. A front carries no
// WireGuard, and asking WireGuard about it reported every working front as
// silent. Its health is read from the panel's own end instead: the supervised
// client that dials it, and whether that client really holds a connection to
// the front's tunnel port — a client retrying against a dead front is alive
// and connected to nothing.
func (s *RelayService) frontHealth(v *RelayView) {
	running, detail := tunnel.GetManager().Status(v.Id)
	pair := tunnel.Pair{
		FrontAddress: v.Address,
		TunnelPort:   v.TunnelPort,
		Transport:    tunnel.Transport(v.Transport),
		Tool:         frontTool(v.Relay),
	}
	known, connected := tunnel.Established(pair)
	v.TunnelUp = running
	// Where the connection cannot be read, the udp transport or a front given
	// by name, the live process is all there is to go on.
	v.Healthy = running && (connected || !known)
	if !running {
		v.TunnelDetail = detail
	}
	if ports, err := s.frontForwardedPorts(v.TunnelPort, tunnel.UDPTunnelPort(pair), v.SSHPort); err == nil && ports != nil {
		v.ForwardedPorts = ports
	}
}

// usage maps an interface name to the inbounds whose countries point at it.
// Deleting a relay out from under a live country would leave those customers
// connecting to a group whose traffic goes nowhere, so the UI is told first.
func (s *RelayService) usage() map[string][]string {
	out := map[string][]string{}
	var inbounds []*model.Inbound
	// Both protocols can point a country at a relay, and deleting one that is
	// still in use has to be refused whichever protocol is using it.
	if err := database.GetDB().Where("protocol IN ?", []model.Protocol{model.Cisco, model.OpenVPN}).Find(&inbounds).Error; err != nil {
		return out
	}
	for _, ib := range inbounds {
		inst, ok := ocserv.InstanceFromInbound(ib)
		if !ok {
			continue
		}
		for _, l := range inst.Locations {
			exit := strings.TrimSpace(l.Exit)
			if exit == "" {
				continue
			}
			name := ib.Remark
			if name == "" {
				name = ib.Tag
			}
			out[exit] = appendOnce(out[exit], name)
		}
	}
	return out
}

// inUse is the list of inbounds offering one relay, never nil.
func inUse(usage map[string][]string, iface string) []string {
	if got := usage[iface]; len(got) > 0 {
		return got
	}
	return []string{}
}

func appendOnce(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

// Add enrolls a new relay. The row appears at once with a pending status and
// the slow part — reaching the server, installing WireGuard — runs behind it:
// an operator who has just typed an address should not watch a spinner for
// four minutes wondering whether the panel has hung.
func (s *RelayService) Add(req RelayRequest) (*model.Relay, error) {
	r, err := s.rowFrom(req, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Password) == "" && strings.TrimSpace(req.PrivateKey) == "" {
		return nil, fmt.Errorf("a password or a private key is needed once, to install the panel's own key")
	}
	panelKey, err := relay.NewPanelKey()
	if err != nil {
		return nil, err
	}
	r.Status = RelayPending
	if err := database.GetDB().Create(r).Error; err != nil {
		return nil, err
	}
	stored, err := encryptRelaySecret(r.Id, panelKey)
	if err != nil {
		return nil, err
	}
	r.WgPrivate = stored
	r.KeyPath = filepath.Join(relayKeyDir(), fmt.Sprintf("relay-%d", r.Id))
	settle := map[string]any{
		"wg_private": r.WgPrivate, "key_path": r.KeyPath,
	}
	// A front's token is minted now, once the id exists to bind the
	// encryption to. Generated, never taken from the request: a token the
	// operator could type is one that ends up in a browser's history.
	if r.Role == model.RelayFront {
		token, terr := tunnel.NewToken()
		if terr != nil {
			return nil, fmt.Errorf("could not generate a tunnel token: %w", terr)
		}
		enc, terr := encryptRelaySecret(r.Id, token)
		if terr != nil {
			return nil, terr
		}
		r.TunnelToken = enc
		settle["tunnel_token"] = enc
	}
	if err := database.GetDB().Model(r).Updates(settle).Error; err != nil {
		return nil, err
	}
	go s.enrollAndProvision(r.Id, req.Password, req.PrivateKey)
	return r, nil
}

// Update changes what the operator can change. New credentials mean a fresh
// enrollment; without them the stored key is kept and the tunnel is simply
// rebuilt wherever the address or port moved.
func (s *RelayService) Update(id int, req RelayRequest) error {
	var existing model.Relay
	if err := database.GetDB().First(&existing, id).Error; err != nil {
		return err
	}
	updated, err := s.rowFrom(req, &existing)
	if err != nil {
		return err
	}
	// Exit and front are different machines, provisioned differently and
	// carrying different state. Flipping one into the other in place would
	// leave the old provisioning behind — an exit's WireGuard interface, or a
	// front's tunnel unit — still running on the server. A role change is a
	// delete and a fresh add, and the panel says so rather than half-doing it.
	if updated.Role != existing.Role {
		return fmt.Errorf("a relay's role cannot be changed; remove it and add it again as %s", updated.Role)
	}
	fields := map[string]any{
		"name": updated.Name, "label": updated.Label, "address": updated.Address,
		"ssh_port": updated.SSHPort, "ssh_user": updated.SSHUser, "enabled": updated.Enabled,
		"status": RelayPending,
	}
	// A front may have its listening port or transport changed; the token is
	// kept as it was.
	if updated.Role == model.RelayFront {
		fields["tunnel_port"] = updated.TunnelPort
		fields["transport"] = updated.Transport
	}
	if err := database.GetDB().Model(&existing).Updates(fields).Error; err != nil {
		return err
	}
	go s.enrollAndProvision(id, req.Password, req.PrivateKey)
	return nil
}

// rowFrom validates what the operator typed. The name reaches a VPN server's
// group list, a filename and a colon-separated file, so it is checked the same
// way a country's name is.
func (s *RelayService) rowFrom(req RelayRequest, base *model.Relay) (*model.Relay, error) {
	name := strings.TrimSpace(req.Name)
	if err := ocserv.ValidLocationName(name); err != nil {
		return nil, err
	}
	address := strings.TrimSpace(req.Address)
	if address == "" {
		return nil, fmt.Errorf("a relay needs an address")
	}
	port := req.SSHPort
	if port <= 0 {
		port = 22
	}
	if port > 65535 {
		return nil, fmt.Errorf("ssh port %d is not a port", port)
	}
	user := strings.TrimSpace(req.SSHUser)
	if user == "" {
		user = "root"
	}
	enabled := true
	if base != nil {
		enabled = base.Enabled
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	// A blank role is an exit, which is what every relay was before fronts
	// existed and what a client that predates the field still sends.
	role := model.RelayExit
	if strings.TrimSpace(req.Role) != "" {
		role = model.RelayRole(strings.TrimSpace(req.Role))
		if !role.Valid() {
			return nil, fmt.Errorf("a relay is an exit or a front, not %q", req.Role)
		}
	}

	r := &model.Relay{
		Name: name, Label: strings.TrimSpace(req.Label), Address: address,
		SSHPort: port, SSHUser: user, Tunnel: "wireguard", Enabled: enabled,
		Role: role,
	}
	if base != nil {
		r.Id = base.Id
		r.KeyPath = base.KeyPath
		r.WgPrivate = base.WgPrivate
	}

	if role == model.RelayFront {
		if err := s.fillFront(r, req, base); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// fillFront settles the fields that only a front has: the port it listens on
// and how the tunnel is carried. The token is left to Add, because it is
// encrypted bound to the relay's id and a new relay has no id until its row
// exists — the same reason the exit's WireGuard key is settled after Create.
//
// On a re-save the stored token is kept, like the exit's key, so the tunnel is
// not rekeyed and every customer riding it is not dropped.
func (s *RelayService) fillFront(r *model.Relay, req RelayRequest, base *model.Relay) error {
	tool, err := tunnel.ParseTool(req.Tool)
	if err != nil {
		return err
	}
	if base != nil {
		// A different tool is a different binary, unit and config on the
		// front, and the old one would be left running beside it. Moving a
		// front from one to the other is a remove and an add.
		was := frontTool(*base)
		if strings.TrimSpace(req.Tool) == "" {
			tool = was
		} else if tool != was {
			return fmt.Errorf("%s runs on %s; remove it and add it again to move it to %s", base.Name, was, tool)
		}
	}
	r.Tunnel = string(tool)

	port := req.TunnelPort
	if port <= 0 {
		port = 3080
	}
	if port > 65535 {
		return fmt.Errorf("tunnel port %d is not a port", port)
	}
	r.TunnelPort = port

	transport := strings.TrimSpace(req.Transport)
	if transport == "" {
		transport = string(tunnel.TransportTCP)
	}
	if err := tunnel.ValidTransportFor(tool, tunnel.Transport(transport)); err != nil {
		return err
	}
	r.Transport = transport
	// Backhaul carries UDP on a second tunnel one port up, which has to be a
	// port too.
	if udp := tunnel.UDPTunnelPort(tunnel.Pair{Tool: tool, Transport: tunnel.Transport(transport), TunnelPort: port}); udp > 65535 {
		return fmt.Errorf("tunnel port %d leaves no room for the UDP tunnel on %d; pick a lower one", port, udp)
	}

	if base != nil {
		r.TunnelToken = base.TunnelToken
	}
	return nil
}

// enrollAndProvision is the slow half, run behind the operator's save. Every
// outcome is written back to the row, because a status that stays "pending"
// for ever is the one failure mode nobody can diagnose.
func (s *RelayService) enrollAndProvision(id int, password, privateKey string) {
	if _, busy := relayRunning.LoadOrStore(id, true); busy {
		return
	}
	defer relayRunning.Delete(id)

	ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()

	s.mark(id, RelayWorking, "")
	var r model.Relay
	if err := database.GetDB().First(&r, id).Error; err != nil {
		return
	}
	if !r.Enabled {
		s.mark(id, RelayDisabled, "")
		return
	}

	creds := relay.Credentials{Address: r.Address, Port: r.SSHPort, User: r.SSHUser, Password: password}
	if strings.TrimSpace(privateKey) != "" {
		creds.PrivateKey = []byte(privateKey)
	}
	if creds.Password != "" || len(creds.PrivateKey) > 0 {
		if err := relay.Enroll(ctx, creds, r.KeyPath); err != nil {
			s.mark(id, RelayError, err.Error())
			logger.Warningf("relay %s: %v", r.Name, err)
			return
		}
	}

	// A front is provisioned as a tunnel, not as a WireGuard exit, and the
	// two share nothing past enrollment. Branch here, after the one SSH key is
	// installed, since getting onto the machine is the same for both.
	if r.Role == model.RelayFront {
		s.provisionFront(ctx, &r)
		return
	}

	panelKey, err := decryptRelaySecret(id, r.WgPrivate)
	if err != nil {
		s.mark(id, RelayError, err.Error())
		return
	}
	peer, err := relay.Provision(ctx, relay.Server{
		Id: r.Id, Name: r.Name, Address: r.Address, Port: r.SSHPort, User: r.SSHUser,
		KeyPath: r.KeyPath, WgPrivate: panelKey,
	})
	if err != nil {
		s.mark(id, RelayError, err.Error())
		logger.Warningf("relay %s: %v", r.Name, err)
		return
	}
	fields := map[string]any{
		"wg_peer_public": peer, "status": RelayReady, "last_error": "", "last_seen": time.Now().Unix(),
	}
	// Where it actually is, and the label a customer will read. The lookup is
	// best effort: a relay that works but whose country nobody will say is
	// still a working relay, and failing the whole enrollment over a missing
	// flag would be absurd.
	if country, code, city, lerr := relay.Locate(ctx, relay.Server{
		Id: r.Id, Name: r.Name, Address: r.Address, Port: r.SSHPort, User: r.SSHUser, KeyPath: r.KeyPath,
	}); lerr == nil {
		fields["country"] = country
		fields["country_code"] = code
		fields["city"] = city
		// Only when the operator left it blank: a label they typed is theirs.
		if strings.TrimSpace(r.Label) == "" {
			if label := relay.LabelFor(country, code); label != "" {
				fields["label"] = label
			}
		}
	} else {
		logger.Warningf("relay %s: could not work out where it is: %v", r.Name, lerr)
	}
	if err := database.GetDB().Model(&model.Relay{Id: id}).Updates(fields).Error; err != nil {
		logger.Warningf("relay %s: could not record the tunnel: %v", r.Name, err)
	}
	logger.Infof("relay %s is ready; traffic for that country leaves through %s", r.Name, r.Address)
}

// provisionFront installs the tunnel on the front machine, brings up the
// panel's own end, and records where the machine turned out to be. It mirrors
// the exit path above: every outcome is written back, because a status stuck
// at "working" for ever is the one failure nobody can act on.
func (s *RelayService) provisionFront(ctx context.Context, r *model.Relay) {
	token, err := decryptRelaySecret(r.Id, r.TunnelToken)
	if err != nil {
		s.mark(r.Id, RelayError, "the tunnel token is unreadable; add the front again: "+err.Error())
		return
	}
	pair, err := s.frontPair(r, token)
	if err != nil {
		s.mark(r.Id, RelayError, err.Error())
		return
	}

	creds, err := frontCreds(*r)
	if err != nil {
		s.mark(r.Id, RelayError, err.Error())
		return
	}

	// The front machine first: it must be listening before the panel dials.
	// Every leg, so a Backhaul front carries UDP as well as TCP.
	for _, leg := range tunnel.Legs(pair) {
		if err := tunnel.Provision(ctx, tunnel.Front{Pair: leg, Credentials: creds}); err != nil {
			s.mark(r.Id, RelayError, err.Error())
			logger.Warningf("front %s: %v", r.Name, err)
			return
		}
	}

	// Then the panel's end. A front that is listening but that the panel
	// never dials carries nothing, so this is as much a part of "ready" as
	// the far side.
	for _, leg := range tunnel.Legs(pair) {
		if err := tunnel.GetManager().Ensure(leg); err != nil {
			s.mark(r.Id, RelayError, "the front is up but the panel could not dial it: "+err.Error())
			logger.Warningf("front %s: %v", r.Name, err)
			return
		}
	}

	fields := map[string]any{
		"status": RelayReady, "last_error": "", "last_seen": time.Now().Unix(),
	}
	if country, code, city, lerr := relay.Locate(ctx, relay.Server{
		Id: r.Id, Name: r.Name, Address: r.Address, Port: r.SSHPort, User: r.SSHUser, KeyPath: r.KeyPath,
	}); lerr == nil {
		fields["country"] = country
		fields["country_code"] = code
		fields["city"] = city
		if strings.TrimSpace(r.Label) == "" {
			if label := relay.LabelFor(country, code); label != "" {
				fields["label"] = label
			}
		}
	} else {
		logger.Warningf("front %s: could not work out where it is: %v", r.Name, lerr)
	}
	if err := database.GetDB().Model(&model.Relay{Id: r.Id}).Updates(fields).Error; err != nil {
		logger.Warningf("front %s: could not record the tunnel: %v", r.Name, err)
	}
	logger.Infof("front %s is ready; customers reach the panel at %s", r.Name, r.Address)
}

// frontCreds builds the SSH credentials for a front from its stored key,
// the same way the exit path reads a key to dial a WireGuard relay. The key
// lives on disk, written once at enrollment; the credentials carry its bytes
// because dialling takes a parsed key, not a path.
func frontCreds(r model.Relay) (relay.Credentials, error) {
	key, err := os.ReadFile(r.KeyPath)
	if err != nil {
		return relay.Credentials{}, fmt.Errorf("the key for %s is missing; add it again: %w", r.Name, err)
	}
	return relay.Credentials{
		Address: r.Address, Port: r.SSHPort, User: r.SSHUser, PrivateKey: key,
	}, nil
}

// frontPair assembles the tunnel description from the relay row and the ports
// the panel's own inbounds are serving right now. The port list is derived,
// never typed, so an inbound added later is carried the moment the next
// reconcile runs — which is the whole reason a customer's new inbound no
// longer silently fails to appear on the front.
func (s *RelayService) frontPair(r *model.Relay, token string) (tunnel.Pair, error) {
	pair := tunnel.Pair{
		Id:           r.Id,
		FrontAddress: r.Address,
		TunnelPort:   r.TunnelPort,
		Transport:    tunnel.Transport(r.Transport),
		Tool:         frontTool(*r),
		Token:        token,
	}
	ports, err := s.frontForwardedPorts(r.TunnelPort, tunnel.UDPTunnelPort(pair), r.SSHPort)
	if err != nil {
		return tunnel.Pair{}, err
	}
	pair.Ports = ports
	return pair, nil
}

// frontTool is the tool a front's row records. Anything unreadable is read as
// Backhaul, which is what every front was before there was a choice.
func frontTool(r model.Relay) tunnel.Tool {
	if tool, err := tunnel.ParseTool(r.Tunnel); err == nil {
		return tool
	}
	return tunnel.ToolBackhaul
}

// frontForwardedPorts is the set of customer-facing ports a front should open,
// taken from the panel's enabled local inbounds and kept clear of the ports
// that must never be forwarded.
func (s *RelayService) frontForwardedPorts(tunnelPort, udpTunnelPort, sshPort int) ([]int, error) {
	var inbounds []*model.Inbound
	if err := database.GetDB().Model(model.Inbound{}).
		Where("node_id IS NULL").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	rows := make([]tunnel.Inbound, 0, len(inbounds))
	for _, ib := range inbounds {
		rows = append(rows, tunnel.Inbound{Port: ib.Port, Enabled: ib.Enable, Local: true})
	}
	var settings SettingService
	panelPort, _ := settings.GetPort()
	reserved := tunnel.Reserved{
		PanelPort:     panelPort,
		TunnelPort:    tunnelPort,
		SSHPort:       sshPort,
		TunnelPortUDP: udpTunnelPort,
	}
	return tunnel.WithACME(tunnel.ForwardedPorts(rows, reserved), reserved), nil
}

func (s *RelayService) mark(id int, status, lastError string) {
	_ = database.GetDB().Model(&model.Relay{Id: id}).Updates(map[string]any{
		"status": status, "last_error": lastError,
	}).Error
}

// SetEnable stops or restarts a country without losing its enrollment. A
// disabled relay has its tunnel taken down here but keeps its key, so turning
// it back on does not ask the operator for a password again.
func (s *RelayService) SetEnable(id int, enable bool) error {
	var r model.Relay
	if err := database.GetDB().First(&r, id).Error; err != nil {
		return err
	}
	if err := database.GetDB().Model(&r).Update("enabled", enable).Error; err != nil {
		return err
	}
	if !enable {
		relay.Down(relay.Server{Id: r.Id})
		s.mark(id, RelayDisabled, "")
		return nil
	}
	go s.enrollAndProvision(id, "", "")
	return nil
}

// Delete removes a relay and takes the panel's footprint off the server. It
// refuses while a cisco inbound still offers it as a country: the alternative
// is a country that stays in every customer's dropdown and leads nowhere.
func (s *RelayService) Delete(id int) error {
	var r model.Relay
	if err := database.GetDB().First(&r, id).Error; err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()

	if r.Role == model.RelayFront {
		// The panel's own end first, so it stops dialling a machine that is
		// about to lose its tunnel. Then the far end, and the row.
		tunnel.GetManager().Remove(r.Id)
		if creds, cerr := frontCreds(r); cerr == nil {
			if err := tunnel.Teardown(ctx, tunnel.Front{Pair: tunnel.Pair{Id: r.Id, Tool: frontTool(r)}, Credentials: creds}); err != nil {
				// The box may be gone already — often why it is being removed.
				logger.Warningf("front %s: %v", r.Name, err)
			}
		} else {
			logger.Warningf("front %s: cannot reach it to tear the tunnel down: %v", r.Name, cerr)
		}
		return database.GetDB().Delete(&model.Relay{}, id).Error
	}

	if used := s.usage()[relay.InterfaceName(r.Id)]; len(used) > 0 {
		return fmt.Errorf("%s is still offered as a country by %s; remove it there first", r.Name, strings.Join(used, ", "))
	}
	if err := relay.Teardown(ctx, relay.Server{
		Id: r.Id, Name: r.Name, Address: r.Address, Port: r.SSHPort, User: r.SSHUser, KeyPath: r.KeyPath,
	}); err != nil {
		// The server may be gone already, which is often why it is being
		// removed. The row goes either way; the operator is told what was
		// left behind so they can clean it up by hand if the box comes back.
		logger.Warningf("relay %s: %v", r.Name, err)
	}
	return database.GetDB().Delete(&model.Relay{}, id).Error
}

// Probe asks a relay what the internet gives it. This is the answer to "will
// my customers be able to open Gemini from this country", and it cannot be
// guessed from the address: the same request from two hosts gets two
// different answers.
func (s *RelayService) Probe(id int) (relay.Reachability, error) {
	var r model.Relay
	if err := database.GetDB().First(&r, id).Error; err != nil {
		return relay.Reachability{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return relay.Reach(ctx, relay.Server{
		Id: r.Id, Name: r.Name, Address: r.Address, Port: r.SSHPort, User: r.SSHUser, KeyPath: r.KeyPath,
	})
}

// Reconcile brings every enabled relay's tunnel back. A WireGuard interface
// does not survive a reboot, and a country whose tunnel is missing after the
// panel restarts would quietly stop working while still being offered.
func (s *RelayService) Reconcile() {
	var rows []model.Relay
	if err := database.GetDB().Where("enabled = ?", true).Find(&rows).Error; err != nil {
		return
	}
	for _, r := range rows {
		// A front is kept up differently: its health is whether the panel's
		// own tunnel client is dialling, and its config is rederived so an
		// inbound added since enrollment is carried without anyone re-saving.
		if r.Role == model.RelayFront {
			s.reconcileFront(r)
			continue
		}
		if r.KeyPath == "" || r.WgPeerPublic == "" {
			continue
		}
		if relay.Check(relay.Server{Id: r.Id}).Healthy() {
			continue
		}
		panelKey, err := decryptRelaySecret(r.Id, r.WgPrivate)
		if err != nil {
			s.mark(r.Id, RelayError, err.Error())
			continue
		}
		if err := relay.Up(relay.Server{
			Id: r.Id, Name: r.Name, Address: r.Address, Port: r.SSHPort, User: r.SSHUser,
			KeyPath: r.KeyPath, WgPrivate: panelKey, WgPeerPublic: r.WgPeerPublic,
		}); err != nil {
			s.mark(r.Id, RelayError, err.Error())
			continue
		}
		s.mark(r.Id, RelayReady, "")
	}
}

// reconcileFront keeps a front's two ends in step with the panel. The panel's
// own client is brought up if it is not dialling, and the far side is
// refreshed so a port added since enrollment is carried — the manager only
// restarts the client when something it reads at startup changed, so a rebuild
// that produces the same tunnel does not drop the customers on it.
func (s *RelayService) reconcileFront(r model.Relay) {
	if r.KeyPath == "" || r.TunnelToken == "" {
		return
	}
	token, err := decryptRelaySecret(r.Id, r.TunnelToken)
	if err != nil {
		s.mark(r.Id, RelayError, err.Error())
		return
	}
	pair, err := s.frontPair(&r, token)
	if err != nil {
		s.mark(r.Id, RelayError, err.Error())
		return
	}

	creds, err := frontCreds(r)
	if err != nil {
		s.mark(r.Id, RelayError, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()
	// The far side carries the derived port set; rerunning this is how an
	// inbound added since enrollment reaches the front without a re-save.
	// The far side restarts only when its config actually changed, so this
	// running every minute does not drop the customers on the tunnel.
	for _, leg := range tunnel.Legs(pair) {
		if err := tunnel.Provision(ctx, tunnel.Front{Pair: leg, Credentials: creds}); err != nil {
			s.mark(r.Id, RelayError, err.Error())
			return
		}
		if err := tunnel.GetManager().Ensure(leg); err != nil {
			s.mark(r.Id, RelayError, err.Error())
			return
		}
	}
	s.mark(r.Id, RelayReady, "")
}

// Options is what the cisco inbound form offers in its exit dropdown: the
// interface to store, and a name the operator recognises.
type RelayOption struct {
	Interface string `json:"interface"`
	Name      string `json:"name"`
	Label     string `json:"label"`
	Address   string `json:"address"`
	// Country and its code travel with the option so the inbound form can
	// fill in a country name and a flag the moment an exit is picked, which
	// is the whole point: the customer should read a flag without anybody
	// typing one.
	Country     string `json:"country"`
	CountryCode string `json:"countryCode"`
	Ready       bool   `json:"ready"`
}

func (s *RelayService) Options() ([]RelayOption, error) {
	var rows []model.Relay
	if err := database.GetDB().Where("enabled = ?", true).Order("name asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]RelayOption, 0, len(rows))
	for _, r := range rows {
		out = append(out, RelayOption{
			Interface:   relay.InterfaceName(r.Id),
			Name:        r.Name,
			Label:       r.Label,
			Address:     r.Address,
			Country:     r.Country,
			CountryCode: r.CountryCode,
			Ready:       r.Status == RelayReady,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	// This server itself, first, with an empty interface — which is exactly
	// how a country that leaves through the panel is already stored, so
	// nothing about the saved shape changes. Offered only when its country is
	// actually known: a guess would label a country wrongly, which is worse
	// than letting the operator type it.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if h := relay.WhereAmI(ctx); h.Known() {
		out = append([]RelayOption{{
			Interface:   "",
			Name:        "local",
			Label:       relay.LabelFor(h.Country, h.CountryCode),
			Address:     "",
			Country:     h.Country,
			CountryCode: h.CountryCode,
			Ready:       true,
		}}, out...)
	}
	return out, nil
}

// encryptRelaySecret stores the panel's tunnel key. The binding is the relay's
// own identity rather than a bare id, so a ciphertext written for a node can
// never be read back as a relay's key.
func encryptRelaySecret(id int, plaintext string) (string, error) {
	if !nodetoken.Enabled() {
		return plaintext, nil
	}
	return nodetoken.EncryptBound([]byte(fmt.Sprintf("relay:%d", id)), plaintext)
}

func decryptRelaySecret(id int, stored string) (string, error) {
	if stored == "" {
		return "", fmt.Errorf("relay %d has no tunnel key; enroll it again", id)
	}
	if !nodetoken.IsEncrypted(stored) {
		return stored, nil
	}
	return nodetoken.DecryptBound([]byte(fmt.Sprintf("relay:%d", id)), stored)
}
