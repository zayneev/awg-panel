package routing

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
)

func TestNFTScriptIsScopedAndOrdered(t *testing.T) {
	cfg := config.Default()
	value := DefaultConfig()
	value.Rules = []model.RoutingRule{
		{ID: "global-direct", Enabled: true, Scope: "global", Domains: []string{"direct.test"}, Outbound: "direct", Priority: 20},
		{ID: "global-warp", Enabled: true, Scope: "global", Domains: []string{"warp.test"}, Outbound: "warp", Priority: 10},
		{ID: "client-warp", Enabled: true, Scope: "clients", Clients: []string{"phone"}, Domains: []string{"client.test"}, Outbound: "warp", Priority: 10},
		{ID: "client-direct", Enabled: true, Scope: "clients", Clients: []string{"phone"}, Domains: []string{"override.test"}, Outbound: "direct", Priority: 20},
	}
	script, err := BuildNFTScript(cfg, value, []model.Client{{Name: "phone", IP: "10.8.0.2", ClientIPv6: "fd00::2"}}, []netip.Addr{netip.MustParseAddr("198.51.100.20")}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`iifname "awg0" udp dport 53`, `iifname != "awg0" return`, "noKernelTun"} {
		if expected == "noKernelTun" {
			continue
		}
		if !strings.Contains(script, expected) {
			t.Fatalf("missing %q\n%s", expected, script)
		}
	}
	clientDirect := strings.Index(script, "@"+RuleSetName("client-direct", false))
	clientWarp := strings.Index(script, "@"+RuleSetName("client-warp", false))
	globalDirect := strings.Index(script, "@"+RuleSetName("global-direct", false))
	globalWarp := strings.Index(script, "@"+RuleSetName("global-warp", false))
	if !(clientDirect < clientWarp && clientWarp < globalDirect && globalDirect < globalWarp) {
		t.Fatalf("wrong rule order\n%s", script)
	}
	if !strings.Contains(script, "ip daddr 198.51.100.20 return") {
		t.Fatal("missing local exclusion")
	}
	for _, expected := range []string{"ip daddr @" + RuleSetName("global-warp", false) + " counter meta mark set 0xa61 tproxy ip to :17890 accept", "ip6 daddr @" + RuleSetName("global-warp", true) + " counter meta mark set 0xa61 tproxy ip6 to :17890 accept"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("missing family-specific tproxy rule %q\n%s", expected, script)
		}
	}
	if strings.Contains(script, " tproxy to :") {
		t.Fatal("found tproxy rule without an explicit address family")
	}
}

func TestNFTScriptIPv4OnlyOmitsIPv6Rules(t *testing.T) {
	value := DefaultConfig()
	value.Rules = []model.RoutingRule{{ID: "global-warp", Enabled: true, Scope: "global", Domains: []string{"warp.test"}, Outbound: "warp"}}
	script, err := BuildNFTScript(config.Default(), value, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "tproxy ip to :17890") {
		t.Fatalf("missing IPv4 tproxy rule\n%s", script)
	}
	if strings.Contains(script, "tproxy ip6") {
		t.Fatalf("IPv4-only script contains an IPv6 tproxy rule\n%s", script)
	}
	if len(policyFamilies(false)) != 1 || len(policyFamilies(true)) != 2 {
		t.Fatal("policy route families do not follow IPv6 availability")
	}
}

func TestIPv6PolicyAvailabilityUsesLoopbackAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ip")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '    inet6 ::1/128 scope host\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if !(Firewall{IPBinary: path}).IPv6PolicyAvailable(context.Background()) {
		t.Fatal("IPv6 loopback address was not detected")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if (Firewall{IPBinary: path}).IPv6PolicyAvailable(context.Background()) {
		t.Fatal("IPv6 was reported available without a loopback address")
	}
}

func TestNFTUnknownClientFailsClosed(t *testing.T) {
	value := DefaultConfig()
	value.Rules = []model.RoutingRule{{ID: "client", Enabled: true, Scope: "clients", Clients: []string{"missing"}, Domains: []string{"example.com"}, Outbound: "warp"}}
	if _, err := BuildNFTScript(config.Default(), value, nil, nil, true); err == nil {
		t.Fatal("unknown client must fail instead of widening the rule")
	}
}
