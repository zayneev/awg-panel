package routing

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.RoutingDir = dir
	cfg.RoutingConfig = filepath.Join(dir, "routing.json")
	cfg.WarpSecrets = filepath.Join(dir, "warp.json")
	cfg.XrayConfig = filepath.Join(dir, "xray.json")
	cfg.GeoSiteData = filepath.Join(dir, "geosite.dat")
	cfg.XrayBinary = filepath.Join(dir, "xray")
	cfg.XrayAssets = dir
	return cfg
}

func TestNormalizeDomainIDNAAndSubdomainForm(t *testing.T) {
	got, err := NormalizeDomain("ПРИМЕР.РФ.")
	if err != nil {
		t.Fatal(err)
	}
	if got != "xn--e1afmkfd.xn--p1ai" {
		t.Fatalf("unexpected IDNA: %s", got)
	}
	for _, bad := range []string{"https://example.com", "-bad.example", "a..example"} {
		if _, err := NormalizeDomain(bad); err == nil {
			t.Errorf("expected %q to fail", bad)
		}
	}
}

func TestStoreAtomicPermissionsAndConcurrentUpdates(t *testing.T) {
	cfg := testConfig(t)
	store := NewStore(cfg)
	if err := store.SaveConfig(DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	const count = 12
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := store.UpdateConfig(func(value *model.RoutingConfig) error {
				value.Rules = append(value.Rules, model.RoutingRule{ID: "r" + string(rune('a'+i)), Enabled: true, Scope: "global", Domains: []string{"example.com"}, Outbound: "warp", Priority: i})
				return nil
			})
			if err != nil {
				t.Errorf("update: %v", err)
			}
		}()
	}
	wg.Wait()
	value, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Rules) != count {
		t.Fatalf("got %d rules, want %d", len(value.Rules), count)
	}
	info, err := os.Stat(cfg.RoutingConfig)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	matches, _ := filepath.Glob(filepath.Join(cfg.RoutingDir, ".awgpanel-*"))
	if len(matches) != 0 {
		t.Fatalf("temporary files left: %v", matches)
	}
}

func TestWarpSecretsAre0600AndNeverInPublicStatus(t *testing.T) {
	cfg := testConfig(t)
	store := NewStore(cfg)
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	warp := WarpSecrets{Version: 1, Source: "imported", PrivateKey: key, Addresses: []string{"172.16.0.2/32"}, MTU: 1280, PeerKey: key, Endpoint: "engage.cloudflareclient.com:2408", AllowedIPs: []string{"0.0.0.0/0"}, AccessToken: "TOP-SECRET-TOKEN", License: "TOP-SECRET-LICENSE", HealthUser: "health", HealthPass: "TOP-SECRET-PASSWORD"}
	if err := store.SaveWarp(warp); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cfg.WarpSecrets)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	status := model.RoutingStatus{Warp: model.WarpStatus{Configured: true, Endpoint: warp.Endpoint}}
	b, _ := json.Marshal(status)
	for _, secret := range []string{warp.PrivateKey, warp.AccessToken, warp.License, warp.HealthPass} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("public JSON leaked secret")
		}
	}
}

func TestUnknownClientCannotBecomeGlobal(t *testing.T) {
	_, err := NormalizeRule(model.RoutingRule{ID: "client", Enabled: true, Scope: "clients", Domains: []string{"example.com"}, Outbound: "warp"})
	if err == nil {
		t.Fatal("client-scoped rule without clients must fail")
	}
}

func TestNetworkOperationsUseInterprocessLock(t *testing.T) {
	cfg := testConfig(t)
	first := NewService(cfg, nil)
	second := NewService(cfg, nil)
	unlock, err := first.acquireNetworkLock(false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if release, err := second.acquireNetworkLock(true); err == nil {
		release()
		t.Fatal("second network operation unexpectedly acquired the lock")
	}
}
