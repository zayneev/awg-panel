package routing

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/net/idna"

	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
	"github.com/zayneev/awg-panel/internal/validate"
)

const routingConfigVersion = 1

type Store struct {
	cfg      config.Config
	lockPath string
}

type WarpSecrets struct {
	Version     int      `json:"version"`
	Source      string   `json:"source"`
	PrivateKey  string   `json:"privateKey"`
	PublicKey   string   `json:"publicKey,omitempty"`
	Addresses   []string `json:"addresses"`
	DNS         []string `json:"dns,omitempty"`
	MTU         int      `json:"mtu"`
	PeerKey     string   `json:"peerPublicKey"`
	Preshared   string   `json:"presharedKey,omitempty"`
	Endpoint    string   `json:"endpoint"`
	AllowedIPs  []string `json:"allowedIPs,omitempty"`
	Reserved    []byte   `json:"reserved,omitempty"`
	DeviceID    string   `json:"deviceId,omitempty"`
	AccessToken string   `json:"accessToken,omitempty"`
	License     string   `json:"license,omitempty"`
	HealthUser  string   `json:"healthUser"`
	HealthPass  string   `json:"healthPassword"`
}

func NewStore(cfg config.Config) *Store {
	return &Store{cfg: cfg, lockPath: filepath.Join(cfg.RoutingDir, ".lock")}
}

func DefaultConfig() model.RoutingConfig {
	return model.RoutingConfig{
		Version:         routingConfigVersion,
		DefaultOutbound: "direct",
		WarpFailure:     "block",
		DNSUpstreams:    []string{"1.1.1.1:53", "8.8.8.8:53"},
		Rules:           []model.RoutingRule{},
	}
}

func (s *Store) LoadConfig() (model.RoutingConfig, error) {
	value := DefaultConfig()
	b, err := os.ReadFile(s.cfg.RoutingConfig)
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return model.RoutingConfig{}, fmt.Errorf("прочитать routing config: %w", err)
	}
	if err := json.Unmarshal(b, &value); err != nil {
		return model.RoutingConfig{}, fmt.Errorf("разобрать routing config: %w", err)
	}
	return NormalizeConfig(value)
}

func (s *Store) SaveConfig(value model.RoutingConfig) error {
	normalized, err := NormalizeConfig(value)
	if err != nil {
		return err
	}
	return s.withLock(func() error {
		return atomicWriteJSON(s.cfg.RoutingConfig, normalized, 0600)
	})
}

func (s *Store) UpdateConfig(fn func(*model.RoutingConfig) error) error {
	return s.withLock(func() error {
		value := DefaultConfig()
		if b, err := os.ReadFile(s.cfg.RoutingConfig); err == nil {
			if err := json.Unmarshal(b, &value); err != nil {
				return fmt.Errorf("разобрать routing config: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := fn(&value); err != nil {
			return err
		}
		normalized, err := NormalizeConfig(value)
		if err != nil {
			return err
		}
		return atomicWriteJSON(s.cfg.RoutingConfig, normalized, 0600)
	})
}

func (s *Store) LoadWarp() (WarpSecrets, error) {
	b, err := os.ReadFile(s.cfg.WarpSecrets)
	if err != nil {
		return WarpSecrets{}, err
	}
	var value WarpSecrets
	if err := json.Unmarshal(b, &value); err != nil {
		return WarpSecrets{}, fmt.Errorf("разобрать WARP config: %w", err)
	}
	if err := ValidateWarp(value); err != nil {
		return WarpSecrets{}, err
	}
	return value, nil
}

func (s *Store) SaveWarp(value WarpSecrets) error {
	if err := ValidateWarp(value); err != nil {
		return err
	}
	return s.withLock(func() error {
		return atomicWriteJSON(s.cfg.WarpSecrets, value, 0600)
	})
}

func (s *Store) ForgetWarp() error {
	return s.withLock(func() error {
		err := os.Remove(s.cfg.WarpSecrets)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		dir, err := os.Open(filepath.Dir(s.cfg.WarpSecrets))
		if err != nil {
			return err
		}
		defer dir.Close()
		return dir.Sync()
	})
}

func (s *Store) withLock(fn func() error) error {
	if err := os.MkdirAll(s.cfg.RoutingDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(s.cfg.RoutingDir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func atomicWriteJSON(path string, value any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".awgpanel-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func NormalizeConfig(value model.RoutingConfig) (model.RoutingConfig, error) {
	if value.Version == 0 {
		value.Version = routingConfigVersion
	}
	if value.Version != routingConfigVersion {
		return value, fmt.Errorf("неподдерживаемая версия routing config: %d", value.Version)
	}
	if value.DefaultOutbound == "" {
		value.DefaultOutbound = "direct"
	}
	if value.DefaultOutbound != "direct" {
		return value, errors.New("в этой версии defaultOutbound должен быть direct")
	}
	if value.WarpFailure == "" {
		value.WarpFailure = "block"
	}
	if value.WarpFailure != "block" {
		return value, errors.New("в этой версии warpFailure должен быть block")
	}
	if len(value.DNSUpstreams) == 0 {
		value.DNSUpstreams = DefaultConfig().DNSUpstreams
	}
	for i, upstream := range value.DNSUpstreams {
		normalized, err := normalizeDNSUpstreamConfig(upstream)
		if err != nil {
			return value, err
		}
		value.DNSUpstreams[i] = normalized
	}
	seen := map[string]struct{}{}
	for i := range value.Rules {
		rule, err := NormalizeRule(value.Rules[i])
		if err != nil {
			return value, fmt.Errorf("правило %q: %w", value.Rules[i].ID, err)
		}
		if _, ok := seen[rule.ID]; ok {
			return value, fmt.Errorf("дублирующийся ID правила %q", rule.ID)
		}
		seen[rule.ID] = struct{}{}
		value.Rules[i] = rule
	}
	sort.SliceStable(value.Rules, func(i, j int) bool {
		if value.Rules[i].Scope != value.Rules[j].Scope {
			return value.Rules[i].Scope == "clients"
		}
		if value.Rules[i].Priority != value.Rules[j].Priority {
			return value.Rules[i].Priority < value.Rules[j].Priority
		}
		return value.Rules[i].ID < value.Rules[j].ID
	})
	return value, nil
}

func normalizeDNSUpstreamConfig(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("некорректный DNS upstream")
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		if addr, parseErr := netip.ParseAddr(value); parseErr == nil {
			host, port = addr.String(), "53"
		} else if !strings.Contains(value, ":") {
			host, port = value, "53"
		} else {
			return "", errors.New("некорректный DNS upstream")
		}
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", errors.New("некорректный порт DNS upstream")
	}
	if _, err := netip.ParseAddr(host); err != nil {
		host, err = NormalizeDomain(host)
		if err != nil {
			return "", fmt.Errorf("некорректный DNS upstream: %w", err)
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(number)), nil
}

func NormalizeRule(rule model.RoutingRule) (model.RoutingRule, error) {
	rule.ID = strings.ToLower(strings.TrimSpace(rule.ID))
	if !ruleIDPattern.MatchString(rule.ID) {
		return rule, errors.New("ID должен содержать 1–63 символа: a-z, 0-9, _ или -")
	}
	if rule.Scope != "global" && rule.Scope != "clients" {
		return rule, errors.New("scope должен быть global или clients")
	}
	if rule.Scope == "global" && len(rule.Clients) != 0 {
		return rule, errors.New("global-правило не может содержать clients")
	}
	if rule.Scope == "clients" && len(rule.Clients) == 0 && rule.Enabled {
		return rule, errors.New("client-правило должно содержать хотя бы одного клиента")
	}
	rule.Clients = uniqueSorted(rule.Clients)
	for _, client := range rule.Clients {
		if err := validate.ClientName(client); err != nil {
			return rule, fmt.Errorf("клиент %q: %w", client, err)
		}
	}
	var domains []string
	for _, domain := range rule.Domains {
		normalized, err := NormalizeDomain(domain)
		if err != nil {
			return rule, err
		}
		domains = append(domains, normalized)
	}
	rule.Domains = uniqueSorted(domains)
	for i, site := range rule.GeoSites {
		site = strings.TrimSpace(site)
		if len(site) >= len("geosite:") && strings.EqualFold(site[:len("geosite:")], "geosite:") {
			site = site[len("geosite:"):]
		}
		site = strings.ToLower(site)
		if !geositePattern.MatchString(site) {
			return rule, fmt.Errorf("некорректная geosite-категория %q", site)
		}
		rule.GeoSites[i] = site
	}
	rule.GeoSites = uniqueSorted(rule.GeoSites)
	if len(rule.Domains) == 0 && len(rule.GeoSites) == 0 {
		return rule, errors.New("нужен хотя бы один domain или geosite")
	}
	if rule.Outbound != "direct" && rule.Outbound != "warp" {
		return rule, errors.New("outbound должен быть direct или warp")
	}
	if rule.Priority < 0 || rule.Priority > 1_000_000 {
		return rule, errors.New("priority должен быть 0–1000000")
	}
	return rule, nil
}

func NormalizeDomain(value string) (string, error) {
	value = strings.TrimSpace(strings.TrimSuffix(value, "."))
	if len(value) >= len("domain:") && strings.EqualFold(value[:len("domain:")], "domain:") {
		value = value[len("domain:"):]
	}
	if value == "" || strings.ContainsAny(value, "/:@\r\n\x00") {
		return "", fmt.Errorf("некорректный домен %q", value)
	}
	ascii, err := idna.Lookup.ToASCII(value)
	if err != nil {
		return "", fmt.Errorf("некорректный домен %q: %w", value, err)
	}
	ascii = strings.ToLower(ascii)
	if len(ascii) > 253 {
		return "", errors.New("домен длиннее 253 символов")
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("некорректный домен %q", value)
		}
	}
	return ascii, nil
}

func ValidateWarp(value WarpSecrets) error {
	if value.Version == 0 {
		value.Version = 1
	}
	if value.Version != 1 || (value.Source != "registered" && value.Source != "imported") {
		return errors.New("некорректный формат WARP config")
	}
	if value.PrivateKey == "" || value.PeerKey == "" || value.Endpoint == "" || len(value.Addresses) == 0 {
		return errors.New("WARP config не содержит обязательные ключи, endpoint или addresses")
	}
	if value.HealthUser == "" || value.HealthPass == "" {
		return errors.New("WARP config не содержит учётные данные health-proxy")
	}
	if value.MTU == 0 {
		value.MTU = 1420
	}
	if value.MTU < 576 || value.MTU > 9000 {
		return errors.New("некорректный WARP MTU")
	}
	if len(value.Reserved) != 0 && len(value.Reserved) != 3 {
		return errors.New("WARP reserved должен содержать 3 байта")
	}
	return nil
}

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
