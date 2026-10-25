package tuic

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"

	serverquic "github.com/apernet/quic-go"
	clientquic "github.com/quic-go/quic-go"
)

func audit3RestartableSOCKS(t *testing.T) (string, *net.UDPConn, *net.UDPAddr) {
	t.Helper()
	u, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	udpAddr := u.LocalAddr().(*net.UDPAddr)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = u.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var greet [4]byte
				if _, err := io.ReadFull(c, greet[:]); err != nil {
					return
				}
				if _, err := c.Write([]byte{5, 0}); err != nil {
					return
				}
				var req [10]byte
				if _, err := io.ReadFull(c, req[:]); err != nil {
					return
				}
				reply := []byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}
				binary.BigEndian.PutUint16(reply[8:], uint16(udpAddr.Port))
				if _, err := c.Write(reply); err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, c)
			}(c)
		}
	}()
	return ln.Addr().String(), u, udpAddr
}

func audit3StartUDPEcho(u *net.UDPConn, arrived chan<- struct{}) {
	go func() {
		buf := make([]byte, 2048)
		for {
			n, src, err := u.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = u.WriteToUDP(buf[:n], src)
			if arrived != nil {
				select {
				case arrived <- struct{}{}:
				default:
				}
			}
		}
	}()
}

func audit3SendPacket(t *testing.T, c *clientquic.Conn, mode uint8, assoc, pkt uint16, payload string) {
	t.Helper()
	var b bytes.Buffer
	if err := WritePacket(&b, assoc, pkt, 1, 0, &Address{Type: AddrTypeIPv4, IP: net.IPv4(8, 8, 8, 8), Port: 53}, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	if mode == packetTransportDatagram {
		if err := c.SendDatagram(b.Bytes()); err != nil {
			t.Fatal(err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := c.OpenUniStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(b.Bytes()); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}

func audit3ReceivePacket(c *clientquic.Conn, mode uint8, duration time.Duration) (*PacketHeader, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	var r io.Reader
	if mode == packetTransportDatagram {
		b, err := c.ReceiveDatagram(ctx)
		if err != nil {
			return nil, nil, err
		}
		r = bytes.NewReader(b)
	} else {
		s, err := c.AcceptUniStream(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer s.CancelRead(0)
		_ = s.SetReadDeadline(time.Now().Add(duration))
		r = s
	}
	if _, _, err := ReadCommand(r); err != nil {
		return nil, nil, err
	}
	h, err := ReadPacketHeader(r)
	if err != nil {
		return nil, nil, err
	}
	p, err := readPacketPayload(r, h)
	return h, p, err
}

func TestAudit3UDPAssociationMustRecoverAfterBridgeReadFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go disables SIO_UDP_CONNRESET on Windows, so a dead UDP bridge never fails a read there")
	}
	for _, mode := range []uint8{packetTransportDatagram, packetTransportStream} {
		name := "datagram"
		if mode == packetTransportStream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			relayAddr, u, udpAddr := audit3RestartableSOCKS(t)
			audit3StartUDPEcho(u, nil)
			s, c, id, _ := startLifecycleTestServer(t, relayAddr, "audit3-recovery@x")
			_, user := authenticatedServerConnection(t, s, id)
			audit3SendPacket(t, c, mode, 42131, 1, "before")
			if _, p, err := audit3ReceivePacket(c, mode, time.Second); err != nil || string(p) != "before" {
				t.Fatalf("initial echo %q %v", p, err)
			}

			s.UpdateRuntimeSettings("recovery-"+name, "bbr", "warn")
			_ = u.Close()
			audit3SendPacket(t, c, mode, 42131, 2, "while-down")
			deadline := time.Now().Add(2 * time.Second)
			for {
				found := false
				for _, line := range logger.GetLogs(10000, "DEBUG") {
					if strings.Contains(line, "recovery-"+name) && strings.Contains(line, "UDP relay receive failed") {
						found = true
						break
					}
				}
				if found {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("closed UDP bridge did not terminate the response reader")
				}
				time.Sleep(time.Millisecond)
			}
			u2, err := net.ListenUDP("udp4", udpAddr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = u2.Close() })
			arrived := make(chan struct{}, 4)
			audit3StartUDPEcho(u2, arrived)
			audit3SendPacket(t, c, mode, 42131, 3, "after")
			select {
			case <-arrived:
			case <-time.After(time.Second):
				t.Fatal("restarted bridge did not receive the retained association request")
			}
			_, payload, oldErr := audit3ReceivePacket(c, mode, 300*time.Millisecond)
			// A different association proves QUIC, the restarted SOCKS bridge, and both transport modes remain functional.
			audit3SendPacket(t, c, mode, 42132, 4, "fresh")
			h, p, newErr := audit3ReceivePacket(c, mode, time.Second)
			if newErr != nil || h.AssocID != 42132 || string(p) != "fresh" {
				t.Fatalf("fresh association probe %v %q %v", h, p, newErr)
			}
			if oldErr != nil || string(payload) != "after" {
				t.Fatalf("retained association became receive blackhole after read failure: response %q err=%v; fresh association works; user up=%d down=%d", payload, oldErr, user.Traffic.BytesUp.Load(), user.Traffic.BytesDown.Load())
			}
		})
	}
}

// audit3EchoSources echoes every datagram the bridge gets back to its sender, and
// returns a wait for the next one's source: a new source is a new association.
func audit3EchoSources(t *testing.T, u *net.UDPConn) func(what string) string {
	sources := make(chan string, 8)
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, src, err := u.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = u.WriteToUDP(buf[:n], src)
			sources <- src.String()
		}
	}()
	return func(what string) string {
		t.Helper()
		select {
		case src := <-sources:
			return src
		case <-time.After(2 * time.Second):
			t.Fatalf("the %s request never reached the UDP bridge", what)
			return ""
		}
	}
}

// A client that stops a response stream mid-write loses that packet only; the
// association, and the bridge socket it holds, carry the next one.
func TestClientStoppingAResponseStreamKeepsTheUDPAssociation(t *testing.T) {
	relayAddr, u, _ := audit3RestartableSOCKS(t)
	nextSource := audit3EchoSources(t, u)
	// quic-go's Write returns with at most a packet (~1.4 KB) still queued, so a
	// 4 KB response under a 16-byte window is mid-write when STOP_SENDING lands.
	const stalledResponse = 4096
	s, c, id, _ := startLifecycleTestServerWith(t, relayAddr, "stopped-response@x", lifecycleSetup{
		instance: func(inst *Instance) { inst.MaxUdpRelayPacketSize = stalledResponse },
		client:   &clientquic.Config{EnableDatagrams: true, InitialStreamReceiveWindow: 16, MaxStreamReceiveWindow: 16},
	})
	authenticatedServerConnection(t, s, id)

	audit3SendPacket(t, c, packetTransportStream, 42141, 1, strings.Repeat("x", stalledResponse))
	firstSource := nextSource("full-size")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stopped, err := c.AcceptUniStream(ctx)
	if err != nil {
		t.Fatalf("the full-size response stream never opened: %v", err)
	}
	if _, command, err := ReadCommand(stopped); err != nil || command != CmdPacket {
		t.Fatalf("full-size response command=%d err=%v", command, err)
	}
	stopped.CancelRead(0)

	audit3SendPacket(t, c, packetTransportStream, 42141, 2, "after-stop")
	_, payload, err := audit3ReceivePacket(c, packetTransportStream, 2*time.Second)
	if err != nil || string(payload) != "after-stop" {
		t.Fatalf("the response after a stopped stream was lost: %q err=%v", payload, err)
	}
	if secondSource := nextSource("after-stop"); secondSource != firstSource {
		t.Fatalf("stopping one response stream rebuilt the association: bridge saw %s, then %s", firstSource, secondSource)
	}
}

// A client may stop a response stream once it has read it, before the server has
// closed it (the order CI met); a packet split over streams must still arrive whole.
func TestClientStoppingAReadFragmentStillGetsTheRestOfThePacket(t *testing.T) {
	relayAddr, u, _ := audit3RestartableSOCKS(t)
	nextSource := audit3EchoSources(t, u)
	var held, stopLanded atomic.Bool
	s, c, id, _ := startLifecycleTestServerWith(t, relayAddr, "stopped-fragment@x", lifecycleSetup{
		instance: func(inst *Instance) { inst.MaxUdpRelayPacketSize = 2 * maxStreamFragmentSize },
		// The first fragment's stream stays unclosed until the client's STOP_SENDING lands.
		server: func(s *Server) {
			s.afterStreamFragmentWrite = func(stream *serverquic.SendStream) {
				if held.Swap(true) {
					return
				}
				select {
				case <-stream.Context().Done():
					stopLanded.Store(true)
				case <-time.After(2 * time.Second):
				}
			}
		},
	})
	authenticatedServerConnection(t, s, id)

	payload := strings.Repeat("y", maxStreamFragmentSize) + "tail"
	audit3SendPacket(t, c, packetTransportStream, 42151, 1, payload)
	nextSource("two-fragment")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var got []byte
	for frag := range 2 {
		stream, err := c.AcceptUniStream(ctx)
		if err != nil {
			t.Fatalf("fragment %d of 2 never arrived: %v", frag, err)
		}
		if _, command, err := ReadCommand(stream); err != nil || command != CmdPacket {
			t.Fatalf("fragment %d command=%d err=%v", frag, command, err)
		}
		h, err := ReadPacketHeader(stream)
		if err != nil || h.FragTotal != 2 || int(h.FragID) != frag {
			t.Fatalf("fragment %d header %+v err=%v", frag, h, err)
		}
		p, err := readPacketPayload(stream, h)
		if err != nil {
			t.Fatalf("fragment %d payload: %v", frag, err)
		}
		got = append(got, p...)
		stream.CancelRead(0)
	}
	if !stopLanded.Load() {
		t.Fatal("the client's stop reached the first stream only after its close; the order under test never happened")
	}
	if string(got) != payload {
		t.Fatalf("reassembled %d bytes, want the %d sent", len(got), len(payload))
	}
}

// audit3EchoOn sends payload over send until its echo returns over want. A
// datagram either way may be lost, so a silent try is resent; an echo on the
// other transport is the failure under test and is never retried.
func audit3EchoOn(t *testing.T, c *clientquic.Conn, send, want uint8, pkt uint16, payload string) {
	t.Helper()
	for try := range uint16(5) {
		audit3SendPacket(t, c, send, 43221, pkt+try, payload)
		if _, p, err := audit3ReceivePacket(c, want, time.Second); err == nil {
			if string(p) != payload {
				t.Fatalf("the echo on transport %d carried %q, want %q", want, p, payload)
			}
			return
		}
		if _, p, err := audit3ReceivePacket(c, 1-want, 50*time.Millisecond); err == nil {
			t.Fatalf("the echo of %q came back on transport %d instead of %d: %q", payload, 1-want, want, p)
		}
	}
	t.Fatalf("no echo of %q on transport %d after 5 sends", payload, want)
}

func TestAudit3FirstTransportReturnsPositiveEchoAfterOppositeModePacket(t *testing.T) {
	for _, first := range []uint8{packetTransportDatagram, packetTransportStream} {
		name := "datagram-first"
		if first == packetTransportStream {
			name = "stream-first"
		}
		t.Run(name, func(t *testing.T) {
			relayAddr, u, _ := audit3RestartableSOCKS(t)
			audit3StartUDPEcho(u, nil)
			_, c, _, _ := startLifecycleTestServer(t, relayAddr, "audit3-first-mode@x")
			audit3EchoOn(t, c, first, first, 1, "first")
			audit3EchoOn(t, c, 1-first, first, 10, "opposite")
		})
	}
}

func TestUDPReaderCleanupCannotDeleteReplacementAssociation(t *testing.T) {
	registry := newUdpAssociationRegistry(1500)
	header := &PacketHeader{AssocID: 7, FragTotal: 1, Size: 1, Addr: &Address{Type: AddrTypeIPv4, IP: net.IPv4(1, 1, 1, 1), Port: 53}}
	old, _, _, complete := registry.feed(packetTransportDatagram, header, []byte("a"))
	if !complete {
		t.Fatal("first packet incomplete")
	}
	registry.dissociate(7)
	replacement, _, _, complete := registry.feed(packetTransportStream, header, []byte("b"))
	if !complete || replacement == old {
		t.Fatal("association generation was reused")
	}
	registry.release(7, old)
	if !registry.touch(7, replacement, time.Now()) {
		t.Fatal("late reader cleanup deleted the new association")
	}
	registry.release(7, replacement)
	if registry.touch(7, replacement, time.Now()) {
		t.Fatal("current reader cleanup retained dead association")
	}
}
