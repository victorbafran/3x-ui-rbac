package tunnel

import "testing"

// The case this file exists for. On the first real tunnel the forwarded ports
// were written by hand; the operator then made an inbound on 10434 and it
// silently did not work, because nothing on the front was listening for it.
// The panel knew the port all along.
func TestANewInboundIsForwardedWithoutAnybodyTypingItsPort(t *testing.T) {
	before := ForwardedPorts([]Inbound{
		{Port: 443, Enabled: true, Local: true},
		{Port: 1194, Enabled: true, Local: true},
	}, Reserved{PanelPort: 2096, TunnelPort: 3080, SSHPort: 22})

	after := ForwardedPorts([]Inbound{
		{Port: 443, Enabled: true, Local: true},
		{Port: 1194, Enabled: true, Local: true},
		{Port: 10434, Enabled: true, Local: true},
	}, Reserved{PanelPort: 2096, TunnelPort: 3080, SSHPort: 22})

	if SamePorts(before, after) {
		t.Fatal("adding an inbound did not change the forwarded ports; the customer would connect to a port nothing is listening on")
	}
	var found bool
	for _, p := range after {
		if p == 10434 {
			found = true
		}
	}
	if !found {
		t.Errorf("10434 is not forwarded: %v", after)
	}
}

// Forwarding the panel's own web port would publish the admin login on the
// front machine's public address. That is a different and much worse thing
// than publishing a VPN port, so it is refused even if an inbound claims it.
func TestThePanelsOwnPortIsNeverForwarded(t *testing.T) {
	got := ForwardedPorts([]Inbound{
		{Port: 2096, Enabled: true, Local: true},
		{Port: 443, Enabled: true, Local: true},
	}, Reserved{PanelPort: 2096, TunnelPort: 3080, SSHPort: 22})

	for _, p := range got {
		if p == 2096 {
			t.Fatalf("the admin panel was forwarded onto the front machine: %v", got)
		}
	}
	if len(got) != 1 || got[0] != 443 {
		t.Errorf("got %v, want only the vpn port", got)
	}
}

// Two more that would each break something quietly: the tunnel's own port
// would send the panel's dial-in back down the tunnel, and the front's ssh
// port would take its operator's way in.
func TestTheTunnelAndSSHPortsAreNeverForwarded(t *testing.T) {
	got := ForwardedPorts([]Inbound{
		{Port: 3080, Enabled: true, Local: true},
		{Port: 22, Enabled: true, Local: true},
		{Port: 443, Enabled: true, Local: true},
	}, Reserved{PanelPort: 2096, TunnelPort: 3080, SSHPort: 22})

	if len(got) != 1 || got[0] != 443 {
		t.Fatalf("got %v, want only 443", got)
	}
}

// A disabled inbound is not served, and an inbound on a node is served
// somewhere else entirely: traffic for either would arrive at the panel and
// find nothing.
func TestOnlyInboundsThisPanelActuallyServesAreForwarded(t *testing.T) {
	got := ForwardedPorts([]Inbound{
		{Port: 443, Enabled: true, Local: true},
		{Port: 1194, Enabled: false, Local: true},
		{Port: 8443, Enabled: true, Local: false},
	}, Reserved{PanelPort: 2096, TunnelPort: 3080, SSHPort: 22})

	if len(got) != 1 || got[0] != 443 {
		t.Fatalf("got %v, want only the enabled local inbound", got)
	}
}

// A reconcile runs constantly. If the same set in a different order read as a
// change, the front would be rewritten and restarted every time — dropping
// every customer on it, forever, for nothing.
func TestTheSameSetInAnotherOrderIsNotAChange(t *testing.T) {
	reserved := Reserved{PanelPort: 2096, TunnelPort: 3080, SSHPort: 22}
	a := ForwardedPorts([]Inbound{
		{Port: 1194, Enabled: true, Local: true},
		{Port: 443, Enabled: true, Local: true},
		{Port: 443, Enabled: true, Local: true},
	}, reserved)
	b := ForwardedPorts([]Inbound{
		{Port: 443, Enabled: true, Local: true},
		{Port: 1194, Enabled: true, Local: true},
	}, reserved)

	if !SamePorts(a, b) {
		t.Fatalf("%v and %v read as different; the tunnel would restart on every reconcile", a, b)
	}
	if len(a) != 2 {
		t.Errorf("the duplicate was not collapsed: %v", a)
	}
}

// Removing the last inbound must produce an empty list rather than the
// previous one, or a front keeps a port open onto a service that is gone.
func TestRemovingEveryInboundEmptiesTheList(t *testing.T) {
	reserved := Reserved{PanelPort: 2096, TunnelPort: 3080, SSHPort: 22}
	full := ForwardedPorts([]Inbound{{Port: 443, Enabled: true, Local: true}}, reserved)
	empty := ForwardedPorts(nil, reserved)

	if len(empty) != 0 {
		t.Fatalf("got %v, want nothing", empty)
	}
	if SamePorts(full, empty) {
		t.Error("an empty list reads the same as a full one, so the front would never be told")
	}
}
