package routing

import (
	"net/netip"
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
	script, err := BuildNFTScript(cfg, value, []model.Client{{Name: "phone", IP: "10.8.0.2", ClientIPv6: "fd00::2"}}, []netip.Addr{netip.MustParseAddr("198.51.100.20")})
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
	if !strings.Contains(script, "ip daddr 198.51.100.20 return") || !strings.Contains(script, "tproxy to :17890") {
		t.Fatalf("missing local exclusion or tproxy")
	}
}

func TestNFTUnknownClientFailsClosed(t *testing.T) {
	value := DefaultConfig()
	value.Rules = []model.RoutingRule{{ID: "client", Enabled: true, Scope: "clients", Clients: []string{"missing"}, Domains: []string{"example.com"}, Outbound: "warp"}}
	if _, err := BuildNFTScript(config.Default(), value, nil, nil); err == nil {
		t.Fatal("unknown client must fail instead of widening the rule")
	}
}
