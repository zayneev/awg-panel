//go:build linux

package routing

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
)

// Run explicitly on a disposable Linux host:
//
//	sudo AWGPANEL_INTEGRATION=1 go test ./internal/routing -run TestNetworkNamespaceRouting -v
func TestNetworkNamespaceRouting(t *testing.T) {
	if os.Getenv("AWGPANEL_INTEGRATION") != "1" {
		t.Skip("set AWGPANEL_INTEGRATION=1")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	for _, binary := range []string{"ip", "nft", "bash"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " is unavailable")
		}
	}
	suffix := strconv.Itoa(os.Getpid())
	server, client := "awgps"+suffix, "awgc"+suffix
	cleanup := func() {
		_ = exec.Command("ip", "netns", "del", client).Run()
		_ = exec.Command("ip", "netns", "del", server).Run()
	}
	cleanup()
	defer cleanup()
	mustRun(t, "ip", "netns", "add", server)
	mustRun(t, "ip", "netns", "add", client)
	mustRun(t, "ip", "link", "add", "awgs"+suffix, "type", "veth", "peer", "name", "awgc"+suffix)
	mustRun(t, "ip", "link", "set", "awgs"+suffix, "netns", server)
	mustRun(t, "ip", "link", "set", "awgc"+suffix, "netns", client)
	mustNS(t, server, "ip", "link", "set", "awgs"+suffix, "name", "awg0")
	mustNS(t, client, "ip", "link", "set", "awgc"+suffix, "name", "eth0")
	for _, ns := range []string{server, client} {
		mustNS(t, ns, "ip", "link", "set", "lo", "up")
	}
	mustNS(t, server, "ip", "addr", "add", "10.77.0.1/24", "dev", "awg0")
	mustNS(t, client, "ip", "addr", "add", "10.77.0.2/24", "dev", "eth0")
	mustNS(t, server, "ip", "-6", "addr", "add", "fd77::1/64", "dev", "awg0")
	mustNS(t, client, "ip", "-6", "addr", "add", "fd77::2/64", "dev", "eth0")
	mustNS(t, server, "ip", "link", "set", "awg0", "up")
	mustNS(t, client, "ip", "link", "set", "eth0", "up")
	mustNS(t, client, "ip", "route", "add", "default", "via", "10.77.0.1")
	mustNS(t, client, "ip", "-6", "route", "add", "default", "via", "fd77::1")

	cfg := config.Default()
	cfg.RoutingInterface = "awg0"
	value := DefaultConfig()
	value.Rules = []model.RoutingRule{
		{ID: "client-direct", Enabled: true, Scope: "clients", Clients: []string{"phone"}, Domains: []string{"direct.test"}, Outbound: "direct", Priority: 1},
		{ID: "global-warp", Enabled: true, Scope: "global", Domains: []string{"warp.test"}, Outbound: "warp", Priority: 2},
	}
	script, err := BuildNFTScript(cfg, value, []model.Client{{Name: "phone", IP: "10.77.0.2", ClientIPv6: "fd77::2"}}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("ip", "netns", "exec", server, "nft", "-f", "-")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("apply nft: %v: %s\n%s", err, output, script)
	}
	mustNS(t, server, "nft", "add", "element", "inet", "awgpanel", RuleSetName("client-direct", false), "{ 9.9.9.9 timeout 60s }")
	mustNS(t, server, "nft", "add", "element", "inet", "awgpanel", RuleSetName("global-warp", false), "{ 8.8.8.8 timeout 60s }")
	mustNS(t, server, "nft", "add", "element", "inet", "awgpanel", RuleSetName("client-direct", true), "{ 2620:fe::fe timeout 60s }")
	mustNS(t, server, "nft", "add", "element", "inet", "awgpanel", RuleSetName("global-warp", true), "{ 2001:4860:4860::8888 timeout 60s }")

	for _, target := range []string{"9.9.9.9", "8.8.8.8", "2620:fe::fe", "2001:4860:4860::8888"} {
		_ = exec.Command("ip", "netns", "exec", client, "bash", "-c", fmt.Sprintf("timeout 1 bash -c 'echo x > /dev/udp/%s/443'", target)).Run()
	}
	output := mustNSOutput(t, server, "nft", "list", "chain", "inet", "awgpanel", "route_warp")
	if strings.Count(output, "packets 1") < 2 && !strings.Contains(output, "packets 2") {
		t.Fatalf("expected direct and WARP counters to move:\n%s", output)
	}

	// Models Xray/DNS failure handling: removing only awgpanel objects leaves the namespace and AWG-facing link intact.
	mustNS(t, server, "nft", "delete", "table", "inet", "awgpanel")
	if err := exec.Command("ip", "netns", "exec", server, "nft", "list", "table", "inet", "awgpanel").Run(); err == nil {
		t.Fatal("emergency removal left nft table behind")
	}
	if output := mustNSOutput(t, server, "ip", "link", "show", "awg0"); !strings.Contains(output, "awg0") {
		t.Fatal("AWG-facing interface was changed")
	}
}

func mustRun(t *testing.T, binary string, args ...string) {
	t.Helper()
	if output, err := exec.Command(binary, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", binary, args, err, output)
	}
}
func mustNS(t *testing.T, ns, binary string, args ...string) {
	t.Helper()
	all := append([]string{"netns", "exec", ns, binary}, args...)
	mustRun(t, "ip", all...)
}
func mustNSOutput(t *testing.T, ns, binary string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all := append([]string{"netns", "exec", ns, binary}, args...)
	output, err := exec.CommandContext(ctx, "ip", all...).CombinedOutput()
	if err != nil {
		t.Fatalf("ip %v: %v: %s", all, err, output)
	}
	return string(bytes.TrimSpace(output))
}
