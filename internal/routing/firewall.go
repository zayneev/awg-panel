package routing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
)

type Firewall struct {
	NFTBinary string
	IPBinary  string
}

func BuildNFTScript(cfg config.Config, value model.RoutingConfig, clients []model.Client, local []netip.Addr, ipv6Policy bool) (string, error) {
	value, err := NormalizeConfig(value)
	if err != nil {
		return "", err
	}
	clientAddresses := make(map[string][]netip.Addr)
	for _, client := range clients {
		for _, raw := range []string{client.IP, client.ClientIPv6} {
			if raw == "" {
				continue
			}
			addr, err := parseAddress(raw)
			if err != nil {
				return "", fmt.Errorf("адрес клиента %s: %w", client.Name, err)
			}
			clientAddresses[client.Name] = append(clientAddresses[client.Name], addr)
		}
	}
	for _, rule := range value.Rules {
		if !rule.Enabled || rule.Scope != "clients" {
			continue
		}
		for _, name := range rule.Clients {
			if len(clientAddresses[name]) == 0 {
				return "", fmt.Errorf("правило %s ссылается на неизвестного клиента %s", rule.ID, name)
			}
		}
	}

	var out strings.Builder
	out.WriteString("table inet awgpanel {\n")
	for _, rule := range value.Rules {
		if !rule.Enabled {
			continue
		}
		fmt.Fprintf(&out, "  set %s { type ipv4_addr; flags timeout; }\n", RuleSetName(rule.ID, false))
		fmt.Fprintf(&out, "  set %s { type ipv6_addr; flags timeout; }\n", RuleSetName(rule.ID, true))
	}
	fmt.Fprintf(&out, "  chain dns_redirect {\n    type nat hook prerouting priority dstnat; policy accept;\n    iifname %q udp dport 53 redirect to :%d\n    iifname %q tcp dport 53 redirect to :%d\n  }\n", cfg.RoutingInterface, cfg.DNSPort, cfg.RoutingInterface, cfg.DNSPort)
	out.WriteString("  chain route_warp {\n    type filter hook prerouting priority mangle; policy accept;\n")
	fmt.Fprintf(&out, "    iifname != %q return\n", cfg.RoutingInterface)
	out.WriteString("    ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.0.0.0/24, 192.0.2.0/24, 192.168.0.0/16, 198.18.0.0/15, 198.51.100.0/24, 203.0.113.0/24, 224.0.0.0/4, 240.0.0.0/4 } return\n")
	out.WriteString("    ip6 daddr { ::/128, ::1/128, fc00::/7, fe80::/10, ff00::/8, 2001:db8::/32 } return\n")
	for _, addr := range local {
		if !addr.IsValid() || addr.IsUnspecified() {
			continue
		}
		if addr.Is4() {
			fmt.Fprintf(&out, "    ip daddr %s return\n", addr)
		} else {
			fmt.Fprintf(&out, "    ip6 daddr %s return\n", addr)
		}
	}
	for _, scope := range []string{"clients", "global"} {
		for _, outbound := range []string{"direct", "warp"} {
			for _, rule := range value.Rules {
				if !rule.Enabled || rule.Scope != scope || rule.Outbound != outbound {
					continue
				}
				v4, v6 := ruleSources(rule, clientAddresses)
				writeNFTRule(&out, rule, false, v4, cfg)
				if ipv6Policy {
					writeNFTRule(&out, rule, true, v6, cfg)
				}
			}
		}
	}
	out.WriteString("  }\n}\n")
	return out.String(), nil
}

func ruleSources(rule model.RoutingRule, clients map[string][]netip.Addr) ([]netip.Addr, []netip.Addr) {
	if rule.Scope == "global" {
		return nil, nil
	}
	var v4, v6 []netip.Addr
	for _, name := range rule.Clients {
		for _, addr := range clients[name] {
			if addr.Is4() {
				v4 = append(v4, addr)
			} else {
				v6 = append(v6, addr)
			}
		}
	}
	return v4, v6
}

func writeNFTRule(out *strings.Builder, rule model.RoutingRule, ipv6 bool, sources []netip.Addr, cfg config.Config) {
	if rule.Scope == "clients" && len(sources) == 0 {
		return
	}
	family := "ip"
	if ipv6 {
		family = "ip6"
	}
	fmt.Fprintf(out, "    meta l4proto { tcp, udp } ")
	if rule.Scope == "clients" {
		values := make([]string, 0, len(sources))
		for _, addr := range sources {
			values = append(values, addr.String())
		}
		sort.Strings(values)
		fmt.Fprintf(out, "%s saddr { %s } ", family, strings.Join(values, ", "))
	}
	fmt.Fprintf(out, "%s daddr @%s ", family, RuleSetName(rule.ID, ipv6))
	if rule.Outbound == "direct" {
		out.WriteString("counter return\n")
		return
	}
	// Older nftables releases do not infer the TPROXY family after an ip/ip6
	// address expression in an inet table. Keep the target address implicit,
	// but state the family explicitly so the rule is accepted by both the old
	// evaluator and newer releases.
	fmt.Fprintf(out, "counter meta mark set 0x%x tproxy %s to :%d accept\n", cfg.FWMark, family, cfg.TProxyPort)
}

func parseAddress(raw string) (netip.Addr, error) {
	raw = strings.TrimSpace(raw)
	if prefix, err := netip.ParsePrefix(raw); err == nil {
		return prefix.Addr().Unmap(), nil
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, err
	}
	return addr.Unmap(), nil
}

func LocalAddresses() []netip.Addr {
	addresses, _ := net.InterfaceAddrs()
	result := make([]netip.Addr, 0, len(addresses))
	for _, item := range addresses {
		if addr, err := parseAddress(item.String()); err == nil {
			result = append(result, addr)
		}
	}
	return result
}

func (f Firewall) Check(ctx context.Context, script string) error {
	dryRun := strings.Replace(script, "table inet awgpanel {", "table inet awgpanel_check {", 1)
	return f.nftInput(ctx, []string{"-c", "-f", "-"}, dryRun)
}

func (f Firewall) Apply(ctx context.Context, script string, cfg config.Config, ipv6Policy bool) error {
	if err := f.Check(ctx, script); err != nil {
		return err
	}
	_ = f.run(ctx, f.nft(), "delete", "table", "inet", "awgpanel")
	if err := f.nftInput(ctx, []string{"-f", "-"}, script); err != nil {
		return err
	}
	if err := f.removePolicy(ctx, cfg); err != nil {
		_ = f.Disable(ctx, cfg)
		return err
	}
	if err := f.addPolicy(ctx, cfg, ipv6Policy); err != nil {
		_ = f.Disable(ctx, cfg)
		return err
	}
	return nil
}

func (f Firewall) Disable(ctx context.Context, cfg config.Config) error {
	var failures []error
	if err := f.run(ctx, f.nft(), "delete", "table", "inet", "awgpanel"); err != nil && !isMissingRule(err) {
		failures = append(failures, err)
	}
	if err := f.removePolicy(ctx, cfg); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (f Firewall) removePolicy(ctx context.Context, cfg config.Config) error {
	var failures []error
	for _, family := range [][]string{{}, {"-6"}} {
		args := append(append([]string{}, family...), "rule", "del", "priority", strconv.Itoa(cfg.RouteTable), "fwmark", fmt.Sprintf("0x%x", cfg.FWMark), "lookup", strconv.Itoa(cfg.RouteTable))
		if err := f.run(ctx, f.ip(), args...); err != nil && !isMissingRule(err) {
			failures = append(failures, err)
		}
		args = append(append([]string{}, family...), "route", "flush", "table", strconv.Itoa(cfg.RouteTable))
		if err := f.run(ctx, f.ip(), args...); err != nil && !isMissingRule(err) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (f Firewall) addPolicy(ctx context.Context, cfg config.Config, ipv6Policy bool) error {
	for _, family := range policyFamilies(ipv6Policy) {
		rule := append(append([]string{}, family...), "rule", "add", "priority", strconv.Itoa(cfg.RouteTable), "fwmark", fmt.Sprintf("0x%x", cfg.FWMark), "lookup", strconv.Itoa(cfg.RouteTable))
		if err := f.run(ctx, f.ip(), rule...); err != nil {
			return err
		}
		route := append(append([]string{}, family...), "route", "add", "local", "default", "dev", "lo", "table", strconv.Itoa(cfg.RouteTable))
		if err := f.run(ctx, f.ip(), route...); err != nil {
			return err
		}
	}
	return nil
}

func policyFamilies(ipv6 bool) [][]string {
	result := [][]string{{}}
	if ipv6 {
		result = append(result, []string{"-6"})
	}
	return result
}

func (f Firewall) IPv6PolicyAvailable(ctx context.Context) bool {
	output, err := exec.CommandContext(ctx, f.ip(), "-6", "addr", "show", "dev", "lo").Output()
	return err == nil && strings.Contains(string(output), "inet6 ")
}

func (f Firewall) nftInput(ctx context.Context, args []string, input string) error {
	command := exec.CommandContext(ctx, f.nft(), args...)
	command.Stdin = strings.NewReader(input)
	var output bytes.Buffer
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("nft %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(output.String()))
	}
	return nil
}

func (f Firewall) run(ctx context.Context, binary string, args ...string) error {
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (f Firewall) nft() string {
	if f.NFTBinary != "" {
		return f.NFTBinary
	}
	return "nft"
}
func (f Firewall) ip() string {
	if f.IPBinary != "" {
		return f.IPBinary
	}
	return "ip"
}

func isMissingRule(err error) bool {
	value := strings.ToLower(err.Error())
	return strings.Contains(value, "no such file") || strings.Contains(value, "no such process") || strings.Contains(value, "does not exist") || strings.Contains(value, "not found") || strings.Contains(value, "cannot find")
}
