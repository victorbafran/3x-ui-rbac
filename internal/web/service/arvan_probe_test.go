package service

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// carriesFrom runs a listener that answers a plain request when plain is set and anything
// else when shaped is set, as a front behind a protocol filter would seem from the panel.
func carriesFrom(t *testing.T, plain, shaped bool) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				n, _ := c.Read(buf)
				if isPlain := strings.HasPrefix(string(buf[:n]), "GET "); (isPlain && plain) || (!isPlain && shaped) {
					_, _ = c.Write([]byte("reply"))
				}
				_, _ = c.Read(buf)
			}()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// An IP that answers plain traffic and drops traffic shaped like the tunnel's is refused;
// one that answers both passes, and one that answers neither is no verdict at all.
func TestAnIPThatDropsTunnelShapedTrafficIsRefused(t *testing.T) {
	saved := carryReplyWait
	carryReplyWait = 300 * time.Millisecond
	t.Cleanup(func() { carryReplyWait = saved })
	for _, tc := range []struct {
		name          string
		plain, shaped bool
		refused       bool
	}{
		{"both answered", true, true, false},
		{"only plain answered", true, false, true},
		{"nothing answered", false, false, false},
	} {
		ip, port := carriesFrom(t, tc.plain, tc.shaped)
		err := carries(context.Background(), ip, port)
		if (err != nil) != tc.refused {
			t.Errorf("%s: %v, want refused %v", tc.name, err, tc.refused)
		}
	}
}

// A refused traffic check fails an IP that passed the dial; a check that could not be made
// leaves the dial's verdict.
func TestTheTrafficCheckDecidesOnlyWhenItRan(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	savedProbe, savedBase := carryProbe, checkHostBase
	t.Cleanup(func() { carryProbe, checkHostBase = savedProbe, savedBase })
	checkHostBase = "http://127.0.0.1:1" // no Iranian vantage points: the dial alone decides
	r := model.Relay{Name: "f", TunnelPort: ln.Addr().(*net.TCPAddr).Port}
	carryProbe = func(context.Context, model.Relay, string) error { return errors.New("filtered") }
	if ok, _, _, err := probeCarries(context.Background(), r, "127.0.0.1"); ok || err == nil {
		t.Fatalf("a filtered IP passed: %v %v", ok, err)
	}
	carryProbe = func(context.Context, model.Relay, string) error { return nil }
	if ok, _, _, err := probeCarries(context.Background(), r, "127.0.0.1"); !ok || err != nil {
		t.Fatalf("an IP whose traffic check found nothing failed: %v %v", ok, err)
	}
}
