package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
)

const (
	minimumDNSTTL = 30 * time.Second
	maximumDNSTTL = time.Hour
)

type DNSSetUpdater interface {
	Add(context.Context, string, netip.Addr, time.Duration) error
}

type DNSProxy struct {
	cfg     config.Config
	routing model.RoutingConfig
	geosite *GeoSiteMatcher
	updater DNSSetUpdater
	nextDNS atomic.Uint64
	fatal   chan error
	fatalMu sync.Once
}

func NewDNSProxy(cfg config.Config, value model.RoutingConfig, updater DNSSetUpdater) (*DNSProxy, error) {
	normalized, err := NormalizeConfig(value)
	if err != nil {
		return nil, err
	}
	matcher, err := LoadGeoSite(cfg.GeoSiteData, requestedGeoSites(normalized))
	if err != nil {
		return nil, err
	}
	return &DNSProxy{cfg: cfg, routing: normalized, geosite: matcher, updater: updater, fatal: make(chan error, 1)}, nil
}

func (p *DNSProxy) ServeDNS(writer dns.ResponseWriter, request *dns.Msg) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	response, err := p.exchange(ctx, request)
	if err != nil {
		response = new(dns.Msg)
		response.SetRcode(request, dns.RcodeServerFailure)
		_ = writer.WriteMsg(response)
		return
	}
	response.Id = request.Id
	if err := p.classify(ctx, request, response); err != nil {
		failure := new(dns.Msg)
		failure.SetRcode(request, dns.RcodeServerFailure)
		_ = writer.WriteMsg(failure)
		p.fatalMu.Do(func() {
			if p.fatal != nil {
				p.fatal <- err
			}
		})
		return
	}
	_ = writer.WriteMsg(response)
}

func (p *DNSProxy) exchange(ctx context.Context, request *dns.Msg) (*dns.Msg, error) {
	var lastErr error
	start := int(p.nextDNS.Add(1)-1) % len(p.routing.DNSUpstreams)
	for offset := range p.routing.DNSUpstreams {
		upstream := normalizeDNSUpstream(p.routing.DNSUpstreams[(start+offset)%len(p.routing.DNSUpstreams)])
		client := &dns.Client{Net: "udp", Timeout: 4 * time.Second}
		response, _, err := client.ExchangeContext(ctx, request, upstream)
		if err == nil && response.Truncated {
			client.Net = "tcp"
			response, _, err = client.ExchangeContext(ctx, request, upstream)
		}
		if err == nil {
			return response, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func normalizeDNSUpstream(value string) string {
	value = strings.TrimSpace(value)
	if _, _, err := net.SplitHostPort(value); err == nil {
		return value
	}
	if addr, err := netip.ParseAddr(value); err == nil && addr.Is6() {
		return "[" + value + "]:53"
	}
	return net.JoinHostPort(value, "53")
}

func (p *DNSProxy) classify(ctx context.Context, request, response *dns.Msg) error {
	if p.updater == nil || response.Rcode != dns.RcodeSuccess {
		return nil
	}
	matched := make(map[string]struct{})
	for _, question := range request.Question {
		for _, rule := range p.matchRules(question.Name) {
			matched[rule.ID] = struct{}{}
		}
	}
	if len(matched) == 0 {
		return nil
	}
	for _, answer := range response.Answer {
		var addr netip.Addr
		var ttl uint32
		switch record := answer.(type) {
		case *dns.A:
			addr, _ = netip.AddrFromSlice(record.A)
			ttl = record.Hdr.Ttl
		case *dns.AAAA:
			addr, _ = netip.AddrFromSlice(record.AAAA)
			ttl = record.Hdr.Ttl
		default:
			continue
		}
		addr = addr.Unmap()
		if !addr.IsValid() || excludedDestination(addr) {
			continue
		}
		duration := clampTTL(ttl)
		for ruleID := range matched {
			if err := p.updater.Add(ctx, RuleSetName(ruleID, addr.Is6()), addr, duration); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *DNSProxy) matchRules(domain string) []model.RoutingRule {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	var matched []model.RoutingRule
	for _, rule := range p.routing.Rules {
		if !rule.Enabled {
			continue
		}
		found := false
		for _, suffix := range rule.Domains {
			if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
				found = true
				break
			}
		}
		if !found {
			for _, category := range rule.GeoSites {
				if p.geosite.Match(category, domain) {
					found = true
					break
				}
			}
		}
		if found {
			matched = append(matched, rule)
		}
	}
	return matched
}

func clampTTL(ttl uint32) time.Duration {
	duration := time.Duration(ttl) * time.Second
	if duration < minimumDNSTTL {
		return minimumDNSTTL
	}
	if duration > maximumDNSTTL {
		return maximumDNSTTL
	}
	return duration
}

func RuleSetName(ruleID string, ipv6 bool) string {
	sum := sha256.Sum256([]byte(ruleID))
	family := "4"
	if ipv6 {
		family = "6"
	}
	return "r_" + hex.EncodeToString(sum[:6]) + "_" + family
}

func excludedDestination(addr netip.Addr) bool {
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified()
}

type NFTSetUpdater struct{ Binary string }

func (u NFTSetUpdater) Add(ctx context.Context, set string, addr netip.Addr, ttl time.Duration) error {
	if !regexpNFTName.MatchString(set) || !addr.IsValid() {
		return errors.New("некорректный nft element")
	}
	if ttl < minimumDNSTTL || ttl > maximumDNSTTL {
		return errors.New("некорректный nft timeout")
	}
	binary := u.Binary
	if binary == "" {
		binary = "nft"
	}
	element := fmt.Sprintf("update element inet awgpanel %s { %s timeout %ds }\n", set, addr.String(), int(ttl/time.Second))
	command := exec.CommandContext(ctx, binary, "-f", "-")
	command.Stdin = strings.NewReader(element)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("nft update element: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func RunDNSProxy(ctx context.Context, proxy *DNSProxy) error {
	handler := dns.HandlerFunc(proxy.ServeDNS)
	hosts := dnsListenHosts(proxy.cfg.DNSListen, Firewall{}.IPv6PolicyAvailable(ctx))
	servers := make([]*dns.Server, 0, len(hosts)*2)
	for _, host := range hosts {
		family := "4"
		if addr, err := netip.ParseAddr(host); err == nil && addr.Is6() {
			family = "6"
		}
		address := net.JoinHostPort(host, strconv.Itoa(proxy.cfg.DNSPort))
		servers = append(servers, &dns.Server{Addr: address, Net: "udp" + family, Handler: handler}, &dns.Server{Addr: address, Net: "tcp" + family, Handler: handler})
	}
	errorsCh := make(chan error, len(servers))
	for _, server := range servers {
		server := server
		go func() { errorsCh <- server.ListenAndServe() }()
	}
	shutdown := func() {
		for _, server := range servers {
			_ = server.Shutdown()
		}
	}
	select {
	case <-ctx.Done():
		shutdown()
		return nil
	case err := <-errorsCh:
		shutdown()
		return err
	case err := <-proxy.fatal:
		shutdown()
		return err
	}
}

func dnsListenHosts(listen string, ipv6 bool) []string {
	hosts := []string{listen}
	if listen == "0.0.0.0" && ipv6 {
		hosts = append(hosts, "::")
	}
	return hosts
}
