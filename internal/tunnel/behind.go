package tunnel

// What an inbound needs when it is served from behind a front rather than
// reached directly. Every value here was paid for in a day of debugging a
// live cisco inbound through a real Backhaul tunnel, and each is the panel
// deciding something the operator should not have to know.

// CiscoDefaults are the knobs a cisco inbound needs flipped once it sits
// behind a front. The panel applies these so the operator does not have to
// learn, remember, or reapply them — which is exactly what went wrong the
// first time: each was set by hand, and one was silently reverted on the
// next save.
type CiscoDefaults struct {
	// MTU is lowered because the front wraps every packet in another layer,
	// so ocserv's usual 1400 pushes past 1500 on the wire and large packets
	// vanish while small ones pass — the connection comes up and then loads
	// nothing. 1300 leaves room for the encapsulation. Measured, not guessed.
	MTU int

	// MaxSameClients is raised because behind a front ocserv sees every
	// client as 127.0.0.1 and cannot recognise a phone returning on a new
	// network. Each network change leaves a session it will not reap for
	// MobileDPD seconds, so a customer with one phone can hold several at
	// once and a tight limit locks them out of their own account.
	MaxSameClients int

	// MobileDPD is cut right down for the same reason: a dead session behind
	// a front is indistinguishable from the phone coming back, so holding it
	// the usual thirty minutes just piles up ghosts. Ninety seconds matches
	// the regular dead-peer timer.
	MobileDPD int

	// DisableUDP is a judgement, not a fix. DTLS works through a tunnel while
	// the path holds, and is faster, but a network change kills it and the
	// client cannot recover without a full reconnect — ocserv ties the DTLS
	// session to a source port the front keeps changing. Left off here: most
	// phones sit on one network, and the cost of a switch is one reconnect,
	// which is better than giving up DTLS for everyone. The operator can turn
	// it on for a fleet that roams constantly.
	DisableUDP bool
}

// behindTunnelCiscoMTU and friends are the measured constants, named so the
// numbers appear once and the reasoning lives on the struct fields above.
const (
	behindTunnelCiscoMTU     = 1300
	behindTunnelCiscoDevices = 5
	behindTunnelCiscoMobDPD  = 90
	// zero in any of these means "the operator set nothing, so use the default"
	directMTUUnchanged       = 0
	directDevicesUnchanged   = 0
	directMobileDPDUnchanged = 0
)

// CiscoBehind returns the settings a cisco inbound should carry when it is
// behind a front, starting from what the operator chose. An operator's own
// value always wins: these are defaults for the common case, not a cage.
//
// `behind` is false for an inbound reached directly, and then nothing is
// changed at all — a direct inbound is exactly what it was before fronts
// existed, which is what keeps every current inbound working untouched.
func CiscoBehind(behind bool, operator CiscoDefaults) CiscoDefaults {
	if !behind {
		return operator
	}
	out := operator
	if out.MTU == directMTUUnchanged {
		out.MTU = behindTunnelCiscoMTU
	}
	if out.MaxSameClients <= directDevicesUnchanged {
		out.MaxSameClients = behindTunnelCiscoDevices
	}
	if out.MobileDPD == directMobileDPDUnchanged {
		out.MobileDPD = behindTunnelCiscoMobDPD
	}
	// DisableUDP is left as the operator set it: the default is DTLS on, and
	// forcing it off here would take speed from the majority to spare the
	// roamers a reconnect.
	return out
}
