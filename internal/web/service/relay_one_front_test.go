package service

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const secondFrontRefusal = "is already the entry server iran, on backpack"

func fakeDNS(t *testing.T, names map[string]string) {
	t.Helper()
	saved := resolveHost
	resolveHost = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if ip, ok := names[host]; ok {
			return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
		}
		return nil, fmt.Errorf("no such host %s", host)
	}
	t.Cleanup(func() { resolveHost = saved })
}

func iranFront() model.Relay {
	return model.Relay{
		Name: "iran", Address: "203.0.113.9", KeyPath: "/k", Tunnel: "backpack",
		Role: model.RelayFront, Enabled: true, PublicName: "tun.example.ir",
	}
}

func TestASecondFrontOnTheSameMachineIsRefused(t *testing.T) {
	setupRelayDB(t)
	fakeDNS(t, map[string]string{"ir1.example.ir": "203.0.113.9"})
	seedRelays(t, iranFront())

	for _, address := range []string{"203.0.113.9", " 203.0.113.9 ", "ir1.example.ir", "IR1.example.ir."} {
		err := (&RelayService{}).refuseASecondFront(&model.Relay{Name: "again", Address: address, Role: model.RelayFront})
		if err == nil || !strings.Contains(err.Error(), secondFrontRefusal) {
			t.Errorf("%q, the same machine, got %v; want the refusal naming iran", address, err)
		}
	}
}

// Refused before a row exists, or a failed add would still be enrolled.
func TestAddRefusesASecondFrontBeforeCreatingIt(t *testing.T) {
	setupRelayDB(t)
	seedRelays(t, iranFront())
	_, err := (&RelayService{}).Add(RelayRequest{
		Name: "again", Address: "203.0.113.9", Role: "front", Tool: "backhaul", Password: "unused",
	})
	if err == nil || !strings.Contains(err.Error(), secondFrontRefusal) {
		t.Fatalf("Add got %v; want the refusal naming iran", err)
	}
	var n int64
	database.GetDB().Model(&model.Relay{}).Count(&n)
	if n != 1 {
		t.Fatalf("the refused front left a row behind: %d rows", n)
	}
}

func TestADifferentMachineIsAnotherFront(t *testing.T) {
	setupRelayDB(t)
	fakeDNS(t, map[string]string{"ir2.example.ir": "203.0.113.77"})
	seedRelays(t, iranFront())
	for _, address := range []string{"203.0.113.77", "ir2.example.ir", "unresolvable.example.ir"} {
		if err := (&RelayService{}).refuseASecondFront(&model.Relay{Name: "two", Address: address, Role: model.RelayFront}); err != nil {
			t.Errorf("%q is a different machine and was refused: %v", address, err)
		}
	}
}

// Several Iranian servers behind one round-robin name is a real setup.
func TestFrontsMayShareAPublicName(t *testing.T) {
	setupRelayDB(t)
	fakeDNS(t, map[string]string{"tun.example.ir": "203.0.113.9"})
	seedRelays(t, iranFront())
	second := &model.Relay{Name: "two", Address: "203.0.113.77", Role: model.RelayFront, PublicName: "tun.example.ir"}
	if err := (&RelayService{}).refuseASecondFront(second); err != nil {
		t.Fatalf("a second machine sharing the public name was refused: %v", err)
	}
}

// Re-saving a front must not be refused as its own duplicate.
func TestAFrontIsNotItsOwnDuplicate(t *testing.T) {
	setupRelayDB(t)
	seedRelays(t, iranFront())
	var stored model.Relay
	if err := database.GetDB().Where("name = ?", "iran").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if err := (&RelayService{}).refuseASecondFront(&stored); err != nil {
		t.Fatalf("re-saving a front was refused as its own duplicate: %v", err)
	}
}

// An exit opens no customer ports, so it may share a front's machine.
func TestAnExitMayShareAFrontsMachine(t *testing.T) {
	setupRelayDB(t)
	seedRelays(t, iranFront())
	exit := &model.Relay{Name: "exit", Address: "203.0.113.9", Role: model.RelayExit}
	if err := (&RelayService{}).refuseASecondFront(exit); err != nil {
		t.Fatalf("an exit was refused for sharing a front's machine: %v", err)
	}
}

// Without its DTLS port on the front, cisco reaches customers on TCP only.
func TestAFrontOpensCiscosOwnDTLSPort(t *testing.T) {
	setupRelayDB(t)
	ib := &model.Inbound{Tag: "cisco", Enable: true, Port: 443, Protocol: model.Cisco, Settings: `{"udpPort":4443}`}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	ports, _, err := (&RelayService{}).frontForwardedPorts(3080, 0, 22)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{443, 4443} {
		if !slices.Contains(ports, want) {
			t.Errorf("the front does not open %d: %v", want, ports)
		}
	}
}

// Each service's own switch decides the leg its port rides: cisco's setting,
// and Xray's per transport or in sockopt. OpenVPN cannot read the header.
func TestTheRealIPPortsAreTheOnesWhoseServiceWantsTheHeader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inbound model.Inbound
		proxied bool
	}{
		{"cisco with real client IP", model.Inbound{Port: 443, Protocol: model.Cisco, Settings: `{"proxyProtocol":true}`}, true},
		{"xray over ws", model.Inbound{Port: 10434, Protocol: model.VLESS, StreamSettings: `{"network":"ws","wsSettings":{"acceptProxyProtocol":true}}`}, true},
		{"xray in sockopt", model.Inbound{Port: 2096, Protocol: model.VLESS, StreamSettings: `{"network":"xhttp","sockopt":{"acceptProxyProtocol":true}}`}, true},
		{"plain xray", model.Inbound{Port: 41330, Protocol: model.VLESS, StreamSettings: `{"network":"tcp"}`}, false},
		{"openvpn, which cannot read it", model.Inbound{Port: 1194, Protocol: model.OpenVPN, Settings: `{"proxyProtocol":true}`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupRelayDB(t)
			ib := tc.inbound
			ib.Tag, ib.Enable = "under-test", true
			if err := database.GetDB().Create(&ib).Error; err != nil {
				t.Fatal(err)
			}
			plain, proxied, err := (&RelayService{}).frontForwardedPorts(3080, 3081, 22)
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(proxied, ib.Port) != tc.proxied || slices.Contains(plain, ib.Port) == tc.proxied {
				t.Errorf("port %d: plain %v, proxied %v; want it proxied=%v", ib.Port, plain, proxied, tc.proxied)
			}
		})
	}
}
