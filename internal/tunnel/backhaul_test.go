package tunnel

import (
	"strings"
	"testing"
)

func goodPair() Pair {
	return Pair{
		Id:           2,
		FrontAddress: "185.0.0.7",
		TunnelPort:   3080,
		Transport:    TransportTCP,
		Token:        "a-real-token",
		Ports:        []int{443, 1194},
	}
}

// The two sides are not symmetrical and swapping them does not degrade the
// tunnel, it prevents it: the near machine must listen and the panel must
// dial. Everything else here assumes that, so it is asserted first.
func TestTheFrontListensAndThePanelDials(t *testing.T) {
	server, err := ServerConfig(goodPair())
	if err != nil {
		t.Fatalf("server config: %v", err)
	}
	if !strings.Contains(server, "[server]") {
		t.Error("the machine customers reach is not configured as the server")
	}
	if !strings.Contains(server, `bind_addr = "0.0.0.0:3080"`) {
		t.Errorf("the front does not listen for the panel:\n%s", server)
	}
	if strings.Contains(server, "remote_addr") {
		t.Errorf("the front is trying to dial out; nothing would ever accept it:\n%s", server)
	}

	client, err := ClientConfig(goodPair())
	if err != nil {
		t.Fatalf("client config: %v", err)
	}
	if !strings.Contains(client, "[client]") {
		t.Error("the panel is not configured as the client")
	}
	if !strings.Contains(client, `remote_addr = "185.0.0.7:3080"`) {
		t.Errorf("the panel does not dial the front:\n%s", client)
	}
	if strings.Contains(client, "bind_addr") {
		t.Errorf("the panel is listening; a network that accepts nothing inbound is the reason this tunnel exists:\n%s", client)
	}
}

// A customer's existing profile should keep working with nothing changed but
// the address, so the front opens the same port numbers the panel serves.
func TestTheCustomerFacingPortsAreMirroredNotRemapped(t *testing.T) {
	server, err := ServerConfig(goodPair())
	if err != nil {
		t.Fatalf("server config: %v", err)
	}
	for _, want := range []string{`"443=127.0.0.1:443"`, `"1194=127.0.0.1:1194"`} {
		if !strings.Contains(server, want) {
			t.Errorf("%s is missing; a customer on that port would have to be reissued:\n%s", want, server)
		}
	}
}

// Backhaul binds each forwarded port. A repeat makes it fail to start, which
// an operator sees as a tunnel that is simply down with no reason given.
func TestARepeatedPortDoesNotStopTheTunnelStarting(t *testing.T) {
	p := goodPair()
	p.Ports = []int{443, 443, 1194, 0, 70000, -1}
	server, err := ServerConfig(p)
	if err != nil {
		t.Fatalf("server config: %v", err)
	}
	if n := strings.Count(server, `"443=`); n != 1 {
		t.Errorf("port 443 is forwarded %d times; Backhaul binds each one:\n%s", n, server)
	}
	for _, unwanted := range []string{`"0=`, `"70000=`, `"-1=`} {
		if strings.Contains(server, unwanted) {
			t.Errorf("%s was written as a port:\n%s", unwanted, server)
		}
	}
}

// Forwarding the tunnel's own port would have the front take the panel's
// dial-in and push it back down the tunnel it arrived on.
func TestTheTunnelsOwnPortIsNotForwardedToItself(t *testing.T) {
	p := goodPair()
	p.Ports = []int{443, p.TunnelPort, 1194}
	server, err := ServerConfig(p)
	if err != nil {
		t.Fatalf("server config: %v", err)
	}
	if strings.Contains(server, `"3080=`) {
		t.Fatalf("the tunnel port forwards into the tunnel:\n%s", server)
	}
	if !strings.Contains(server, `"443=`) || !strings.Contains(server, `"1194=`) {
		t.Errorf("the real ports were dropped with it:\n%s", server)
	}
}

// Each refusal is a tunnel that would come up looking fine and do the wrong
// thing, so each one is checked by its reason rather than by "an error
// happened".
func TestAMisconfiguredTunnelIsRefusedWithTheReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Pair)
		want string
	}{
		{"no token", func(p *Pair) { p.Token = " " }, "anyone who finds the tunnel port"},
		{"no ports", func(p *Pair) { p.Ports = nil }, "carry nothing"},
		{"no address", func(p *Pair) { p.FrontAddress = "" }, "no address"},
		{"a transport Backhaul does not know", func(p *Pair) { p.Transport = "quic" }, "not a transport"},
		{"no transport", func(p *Pair) { p.Transport = "" }, "no transport"},
		{"an impossible port", func(p *Pair) { p.TunnelPort = 70000 }, "is not a port"},
		{"no id", func(p *Pair) { p.Id = 0 }, "cannot collide"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := goodPair()
			tc.edit(&p)
			_, err := ServerConfig(p)
			if err == nil {
				t.Fatalf("accepted a tunnel with %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refused for some other reason than %s: %v", tc.name, err)
			}
		})
	}
}

// quic and kcp are Backhaul transports in a sibling project, not this one.
// Offering a name the binary does not know produces a tunnel that will not
// start, with nothing in the panel to say why.
func TestOnlyTheTransportsBackhaulKnowsAreAccepted(t *testing.T) {
	for _, good := range []Transport{
		TransportTCP, TransportTCPMux, TransportUDP, TransportWS,
		TransportWSMux, TransportWSS, TransportWSSMux, TransportUDPoTCP,
	} {
		if err := ValidTransport(good); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
	for _, bad := range []Transport{"quic", "kcp", "pck", "stealth", "TCP", ""} {
		if err := ValidTransport(bad); err == nil {
			t.Errorf("%q was accepted; Backhaul would refuse to start on it", bad)
		}
	}
}

// Two tunnels on one panel must not share a file or a unit. The first version
// of this idea in another project wrote one fixed name for both: the second
// tunnel overwrote the first one's config, restarted over its unit, and took
// it down.
func TestTwoTunnelsShareNoFileAndNoUnit(t *testing.T) {
	if UnitName(1) == UnitName(2) {
		t.Fatalf("both tunnels use the unit %q; restarting one stops the other", UnitName(1))
	}
	if RemoteConfigPath(1) == RemoteConfigPath(2) {
		t.Fatalf("both tunnels write %q; standing up the second erases the first", RemoteConfigPath(1))
	}
	if !strings.Contains(RemoteConfigPath(7), "7") || !strings.Contains(UnitName(7), "7") {
		t.Error("the tunnel's own id is not in its file or unit name")
	}
}
