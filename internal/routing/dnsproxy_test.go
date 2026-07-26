package routing

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
)

type capturedElement struct {
	set  string
	addr netip.Addr
	ttl  time.Duration
}
type captureUpdater struct{ values []capturedElement }

func (u *captureUpdater) Add(_ context.Context, set string, addr netip.Addr, ttl time.Duration) error {
	u.values = append(u.values, capturedElement{set, addr, ttl})
	return nil
}

type failingUpdater struct{}

func (failingUpdater) Add(context.Context, string, netip.Addr, time.Duration) error {
	return errors.New("nft unavailable")
}

func TestDNSClassificationTTLAndSubdomains(t *testing.T) {
	value := DefaultConfig()
	value.Rules = []model.RoutingRule{{ID: "warp", Enabled: true, Scope: "global", Domains: []string{"example.com"}, Outbound: "warp", Priority: 10}}
	updater := &captureUpdater{}
	proxy := &DNSProxy{cfg: config.Default(), routing: value, geosite: &GeoSiteMatcher{categories: map[string][]geoPattern{}}, updater: updater}
	request := new(dns.Msg)
	request.SetQuestion("www.example.com.", dns.TypeA)
	response := new(dns.Msg)
	response.SetReply(request)
	response.Answer = []dns.RR{
		&dns.A{Hdr: dns.RR_Header{Name: "www.example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 1}, A: net.ParseIP("10.0.0.8")},
		&dns.A{Hdr: dns.RR_Header{Name: "www.example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 7200}, A: net.ParseIP("8.8.8.8")},
	}
	if err := proxy.classify(context.Background(), request, response); err != nil {
		t.Fatal(err)
	}
	if len(updater.values) != 1 {
		t.Fatalf("got %d public elements; documentation IP must be excluded", len(updater.values))
	}
	if updater.values[0].ttl != time.Hour {
		t.Fatalf("ttl=%s", updater.values[0].ttl)
	}
	if updater.values[0].set != RuleSetName("warp", false) {
		t.Fatalf("set=%s", updater.values[0].set)
	}
	if clampTTL(0) != 30*time.Second || clampTTL(100) != 100*time.Second || clampTTL(99999) != time.Hour {
		t.Fatal("TTL clamp mismatch")
	}
}

func TestUnmatchedDNSDoesNotPopulateSets(t *testing.T) {
	value := DefaultConfig()
	value.Rules = []model.RoutingRule{{ID: "warp", Enabled: true, Scope: "global", Domains: []string{"example.com"}, Outbound: "warp"}}
	updater := &captureUpdater{}
	proxy := &DNSProxy{routing: value, geosite: &GeoSiteMatcher{categories: map[string][]geoPattern{}}, updater: updater}
	request := new(dns.Msg)
	request.SetQuestion("other.test.", dns.TypeA)
	response := new(dns.Msg)
	response.SetReply(request)
	response.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "other.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("8.8.4.4")}}
	if err := proxy.classify(context.Background(), request, response); err != nil {
		t.Fatal(err)
	}
	if len(updater.values) != 0 {
		t.Fatal("unmatched domain populated a set")
	}
}

func TestMatchedDNSFailsClosedWhenNFTUpdateFails(t *testing.T) {
	value := DefaultConfig()
	value.Rules = []model.RoutingRule{{ID: "warp", Enabled: true, Scope: "global", Domains: []string{"example.com"}, Outbound: "warp"}}
	proxy := &DNSProxy{routing: value, geosite: &GeoSiteMatcher{categories: map[string][]geoPattern{}}, updater: failingUpdater{}}
	request := new(dns.Msg)
	request.SetQuestion("example.com.", dns.TypeA)
	response := new(dns.Msg)
	response.SetReply(request)
	response.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("8.8.8.8")}}
	if err := proxy.classify(context.Background(), request, response); err == nil {
		t.Fatal("matched response must not be returned when nft classification fails")
	}
}
