package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
)

const (
	dnsUnit           = "awgpanel-routing-dns.service"
	xrayUnit          = "awgpanel-routing-xray.service"
	rollbackUnit      = "awgpanel-routing-rollback.timer"
	appliedDigestPath = "/run/awgpanel-routing/applied.sha256"
)

type ClientProvider func(context.Context) ([]model.Client, error)

type RoutingService struct {
	cfg             config.Config
	store           *Store
	firewall        Firewall
	clients         ClientProvider
	mu              sync.Mutex
	networkLockPath string
}

func NewService(cfg config.Config, clients ClientProvider) *RoutingService {
	return &RoutingService{cfg: cfg, store: NewStore(cfg), firewall: Firewall{}, clients: clients, networkLockPath: filepath.Join(cfg.RoutingDir, ".network.lock")}
}

func (s *RoutingService) Status(ctx context.Context) model.RoutingStatus {
	result := model.RoutingStatus{State: "disabled", CheckedAt: time.Now().UTC()}
	_, xrayErr := os.Stat(s.cfg.XrayBinary)
	_, geoErr := os.Stat(s.cfg.GeoSiteData)
	_, dnsUnitErr := os.Stat(filepath.Join("/etc/systemd/system", dnsUnit))
	_, xrayUnitErr := os.Stat(filepath.Join("/etc/systemd/system", xrayUnit))
	result.Installed = xrayErr == nil && geoErr == nil && dnsUnitErr == nil && xrayUnitErr == nil
	value, err := s.store.LoadConfig()
	if err != nil {
		result.State, result.LastError = "error", err.Error()
		result.Check = model.RoutingCheck{Errors: []string{err.Error()}}
		return result
	}
	result.Enabled = value.Enabled
	for _, rule := range value.Rules {
		if rule.Enabled {
			result.Rules++
		}
	}
	result.DNSActive = unitActive(ctx, dnsUnit)
	result.XrayActive = unitActive(ctx, xrayUnit)
	result.FirewallActive = nftTableExists(ctx)
	if warp, err := s.store.LoadWarp(); err == nil {
		result.Warp = model.WarpStatus{Configured: true, Source: warp.Source, Endpoint: warp.Endpoint, Addresses: append([]string{}, warp.Addresses...)}
		if result.XrayActive {
			healthCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			status, healthErr := CheckWarpHealth(healthCtx, s.cfg, warp)
			cancel()
			result.Warp = status
			if healthErr != nil {
				result.LastError = healthErr.Error()
			}
		}
	}
	if !value.Enabled {
		result.State = "disabled"
	} else if result.DNSActive && result.XrayActive && result.FirewallActive && result.Warp.Healthy {
		result.State = "active"
	} else if !result.FirewallActive {
		result.State = "degraded_direct"
	} else {
		result.State = "degraded_warp_blocked"
	}
	result.NeedsApply = value.Enabled && (!result.DNSActive || !result.XrayActive || !result.FirewallActive || !isApplied(value))
	result.Check.OK = result.State == "active" || result.State == "disabled"
	if !result.Check.OK {
		result.Check.Warnings = []string{"routing state: " + result.State}
	}
	return result
}

func (s *RoutingService) Check(ctx context.Context) model.RoutingCheck {
	check := model.RoutingCheck{OK: true}
	value, err := s.store.LoadConfig()
	if err != nil {
		check.Errors = append(check.Errors, err.Error())
		check.OK = false
		return check
	}
	if _, err := exec.LookPath("nft"); err != nil {
		check.Errors = append(check.Errors, "не найден nft")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		check.Errors = append(check.Errors, "не найден ip")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		check.Errors = append(check.Errors, "не найден systemctl")
	}
	if info, err := os.Stat(s.cfg.XrayBinary); err != nil || info.Mode()&0111 == 0 {
		check.Errors = append(check.Errors, "Xray v26.7.11 не установлен или не исполняемый")
	} else if output, err := exec.CommandContext(ctx, s.cfg.XrayBinary, "version").Output(); err != nil || !strings.Contains(string(output), "Xray 26.7.11") {
		check.Errors = append(check.Errors, "требуется отдельный Xray версии 26.7.11")
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", s.cfg.RoutingInterface)); err != nil {
		check.Errors = append(check.Errors, "интерфейс "+s.cfg.RoutingInterface+" не найден")
	}
	if _, err := LoadGeoSite(s.cfg.GeoSiteData, requestedGeoSites(value)); err != nil {
		check.Errors = append(check.Errors, err.Error())
	}
	warp, warpErr := s.store.LoadWarp()
	if needsWarp(value) && warpErr != nil {
		check.Errors = append(check.Errors, "WARP не настроен: "+warpErr.Error())
	}
	clients, clientsErr := s.clientList(ctx)
	if clientsErr != nil {
		check.Errors = append(check.Errors, "прочитать клиентов: "+clientsErr.Error())
	}
	if clientsErr == nil {
		if script, err := BuildNFTScript(s.cfg, value, clients, LocalAddresses()); err != nil {
			check.Errors = append(check.Errors, err.Error())
		} else if _, err := exec.LookPath("nft"); err == nil {
			if err := s.firewall.Check(ctx, script); err != nil {
				check.Errors = append(check.Errors, err.Error())
			}
		}
	}
	if warpErr == nil {
		if err := s.checkXrayValue(ctx, warp); err != nil {
			check.Errors = append(check.Errors, err.Error())
		}
	}
	if !unitActive(ctx, dnsUnit) {
		hosts := []string{s.cfg.DNSListen}
		if s.cfg.DNSListen == "0.0.0.0" {
			hosts = append(hosts, "::")
		}
		for _, host := range hosts {
			if err := portAvailable(host, s.cfg.DNSPort, true); err != nil {
				check.Errors = append(check.Errors, fmt.Sprintf("DNS-порт %s занят: %v", host, err))
			}
		}
	}
	if !unitActive(ctx, xrayUnit) {
		if err := portAvailable("0.0.0.0", s.cfg.TProxyPort, true); err != nil {
			check.Errors = append(check.Errors, fmt.Sprintf("TProxy-порт занят: %v", err))
		}
		if err := portAvailable("127.0.0.1", s.cfg.HealthPort, false); err != nil {
			check.Errors = append(check.Errors, fmt.Sprintf("health-порт занят: %v", err))
		}
	}
	if nftTableExists(ctx) {
		if !value.Enabled {
			check.Errors = append(check.Errors, "таблица inet awgpanel уже существует при выключенной маршрутизации")
		} else if !nftTableOwned(ctx) {
			check.Errors = append(check.Errors, "таблица inet awgpanel не похожа на таблицу этой панели")
		}
	}
	check.Errors = append(check.Errors, policyConflicts(ctx, s.cfg, value.Enabled)...)
	if value.Enabled && (!unitActive(ctx, dnsUnit) || !unitActive(ctx, xrayUnit)) {
		check.Warnings = append(check.Warnings, "routing включён в конфигурации, но одна из служб не активна")
	}
	check.OK = len(check.Errors) == 0
	return check
}

func (s *RoutingService) Enable(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.acquireNetworkLock(false)
	if err != nil {
		return err
	}
	defer unlock()
	value, err := s.store.LoadConfig()
	if err != nil {
		return err
	}
	if check := s.Check(ctx); !check.OK {
		return errors.New(strings.Join(check.Errors, "; "))
	}
	warp, err := s.store.LoadWarp()
	if err != nil {
		return err
	}
	if err := WriteXrayConfig(s.cfg, warp); err != nil {
		return err
	}
	if err := s.startRollback(ctx); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = s.emergencyDisable(context.Background())
		}
	}()
	if err := systemctl(ctx, "start", xrayUnit); err != nil {
		return err
	}
	healthCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := waitWarpHealth(healthCtx, s.cfg, warp); err != nil {
		return err
	}
	clients, err := s.clientList(ctx)
	if err != nil {
		return err
	}
	script, err := BuildNFTScript(s.cfg, value, clients, LocalAddresses())
	if err != nil {
		return err
	}
	if err := s.firewall.Apply(ctx, script, s.cfg); err != nil {
		return err
	}
	if err := systemctl(ctx, "start", dnsUnit); err != nil {
		return err
	}
	if !unitActive(ctx, dnsUnit) {
		return errors.New("DNS-классификатор завершился сразу после запуска")
	}
	value.Enabled = true
	if err := s.store.SaveConfig(value); err != nil {
		return err
	}
	if err := markApplied(value); err != nil {
		return err
	}
	_ = systemctl(ctx, "stop", rollbackUnit)
	committed = true
	return nil
}

func (s *RoutingService) Apply(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.acquireNetworkLock(false)
	if err != nil {
		return err
	}
	defer unlock()
	value, err := s.store.LoadConfig()
	if err != nil {
		return err
	}
	if !value.Enabled {
		return errors.New("маршрутизация выключена; сначала выполните routing enable")
	}
	if check := s.Check(ctx); !check.OK {
		return errors.New(strings.Join(check.Errors, "; "))
	}
	warp, err := s.store.LoadWarp()
	if err != nil {
		return err
	}
	if err := WriteXrayConfig(s.cfg, warp); err != nil {
		return err
	}
	if err := CheckXrayConfig(ctx, s.cfg); err != nil {
		return err
	}
	if err := systemctl(ctx, "restart", xrayUnit); err != nil {
		_ = s.firewall.Disable(ctx, s.cfg)
		return err
	}
	if err := waitWarpHealth(ctx, s.cfg, warp); err != nil {
		_ = s.firewall.Disable(ctx, s.cfg)
		return err
	}
	if err := systemctl(ctx, "restart", dnsUnit); err != nil {
		_ = s.firewall.Disable(ctx, s.cfg)
		return err
	}
	clients, err := s.clientList(ctx)
	if err != nil {
		return err
	}
	script, err := BuildNFTScript(s.cfg, value, clients, LocalAddresses())
	if err != nil {
		return err
	}
	if err := s.firewall.Apply(ctx, script, s.cfg); err != nil {
		return err
	}
	return markApplied(value)
}

func (s *RoutingService) Disable(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, lockErr := s.acquireNetworkLock(false)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	value, err := s.store.LoadConfig()
	if err == nil {
		value.Enabled = false
		err = s.store.SaveConfig(value)
	}
	return errors.Join(err, s.emergencyDisable(ctx))
}

func (s *RoutingService) EmergencyDisable(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.acquireNetworkLock(false)
	if err != nil {
		return err
	}
	defer unlock()
	return s.emergencyDisable(ctx)
}

func (s *RoutingService) RemoveIntercept(ctx context.Context) error {
	unlock, err := s.acquireNetworkLock(true)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()
	_ = os.Remove(appliedDigestPath)
	return s.firewall.Disable(ctx, s.cfg)
}

func (s *RoutingService) emergencyDisable(ctx context.Context) error {
	_ = os.Remove(appliedDigestPath)
	firewallErr := s.firewall.Disable(ctx, s.cfg)
	xrayErr := systemctlIgnoreMissing(ctx, "stop", xrayUnit)
	dnsErr := systemctlIgnoreMissing(ctx, "stop", dnsUnit)
	_ = systemctlIgnoreMissing(ctx, "stop", rollbackUnit)
	return errors.Join(firewallErr, xrayErr, dnsErr)
}

func (s *RoutingService) Rules() ([]model.RoutingRule, error) {
	value, err := s.store.LoadConfig()
	if err != nil {
		return nil, err
	}
	return value.Rules, nil
}

func (s *RoutingService) AddRule(rule model.RoutingRule) error {
	return s.store.UpdateConfig(func(value *model.RoutingConfig) error {
		for _, existing := range value.Rules {
			if existing.ID == strings.ToLower(strings.TrimSpace(rule.ID)) {
				return fmt.Errorf("правило %s уже существует", rule.ID)
			}
		}
		value.Rules = append(value.Rules, rule)
		return nil
	})
}

func (s *RoutingService) SetRule(rule model.RoutingRule) error {
	normalized, err := NormalizeRule(rule)
	if err != nil {
		return err
	}
	rule = normalized
	return s.store.UpdateConfig(func(value *model.RoutingConfig) error {
		for i := range value.Rules {
			if value.Rules[i].ID == rule.ID {
				value.Rules[i] = rule
				return nil
			}
		}
		return fmt.Errorf("правило %s не найдено", rule.ID)
	})
}

func (s *RoutingService) ToggleRule(id string, enabled bool) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if !ruleIDPattern.MatchString(id) {
		return errors.New("некорректный ID правила")
	}
	return s.store.UpdateConfig(func(value *model.RoutingConfig) error {
		for i := range value.Rules {
			if value.Rules[i].ID == id {
				value.Rules[i].Enabled = enabled
				return nil
			}
		}
		return fmt.Errorf("правило %s не найдено", id)
	})
}

func (s *RoutingService) DeleteRule(id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if !ruleIDPattern.MatchString(id) {
		return errors.New("некорректный ID правила")
	}
	return s.store.UpdateConfig(func(value *model.RoutingConfig) error {
		for i := range value.Rules {
			if value.Rules[i].ID == id {
				value.Rules = append(value.Rules[:i], value.Rules[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("правило %s не найдено", id)
	})
}

func (s *RoutingService) WarpRegister(ctx context.Context, acceptTerms bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.acquireNetworkLock(false)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.requireRoutingDisabled(); err != nil {
		return err
	}
	value, err := RegisterWarp(ctx, acceptTerms)
	if err != nil {
		return err
	}
	if err := s.store.SaveWarp(value); err != nil {
		return err
	}
	return WriteXrayConfig(s.cfg, value)
}

func (s *RoutingService) WarpImport(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.acquireNetworkLock(false)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.requireRoutingDisabled(); err != nil {
		return err
	}
	value, err := ImportWGQuick(path)
	if err != nil {
		return err
	}
	if err := s.store.SaveWarp(value); err != nil {
		return err
	}
	return WriteXrayConfig(s.cfg, value)
}

func (s *RoutingService) WarpTest(ctx context.Context) (model.WarpStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.acquireNetworkLock(false)
	if err != nil {
		return model.WarpStatus{}, err
	}
	defer unlock()
	warp, err := s.store.LoadWarp()
	if err != nil {
		return model.WarpStatus{}, err
	}
	started := !unitActive(ctx, xrayUnit)
	if started {
		if err := WriteXrayConfig(s.cfg, warp); err != nil {
			return model.WarpStatus{}, err
		}
		if err := systemctl(ctx, "start", xrayUnit); err != nil {
			return model.WarpStatus{}, err
		}
		value, _ := s.store.LoadConfig()
		if !value.Enabled {
			defer systemctlIgnoreMissing(context.Background(), "stop", xrayUnit)
		}
	}
	status, healthErr := CheckWarpHealth(ctx, s.cfg, warp)
	if healthErr != nil || !started {
		return status, healthErr
	}
	value, err := s.store.LoadConfig()
	if err != nil || !value.Enabled {
		return status, err
	}
	if !unitActive(ctx, dnsUnit) {
		return status, nil
	}
	clients, err := s.clientList(ctx)
	if err != nil {
		return status, err
	}
	script, err := BuildNFTScript(s.cfg, value, clients, LocalAddresses())
	if err != nil {
		return status, err
	}
	if err := s.firewall.Apply(ctx, script, s.cfg); err != nil {
		return status, err
	}
	return status, markApplied(value)
}

func (s *RoutingService) WarpForget(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.acquireNetworkLock(false)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.requireRoutingDisabled(); err != nil {
		return err
	}
	_ = systemctlIgnoreMissing(ctx, "stop", xrayUnit)
	_ = os.Remove(s.cfg.XrayConfig)
	return s.store.ForgetWarp()
}

func (s *RoutingService) requireRoutingDisabled() error {
	value, err := s.store.LoadConfig()
	if err != nil {
		return err
	}
	if value.Enabled {
		return errors.New("сначала выключите маршрутизацию")
	}
	return nil
}

func (s *RoutingService) RunDNS(ctx context.Context) error {
	value, err := s.store.LoadConfig()
	if err != nil {
		return err
	}
	proxy, err := NewDNSProxy(s.cfg, value, NFTSetUpdater{})
	if err != nil {
		return err
	}
	return RunDNSProxy(ctx, proxy)
}

func (s *RoutingService) Recover(ctx context.Context) error {
	value, err := s.store.LoadConfig()
	if err != nil || !value.Enabled {
		return err
	}
	unlock, err := s.acquireNetworkLock(true)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()
	warp, err := s.store.LoadWarp()
	if err != nil {
		return err
	}
	if err := waitWarpHealth(ctx, s.cfg, warp); err != nil {
		return err
	}
	clients, err := s.clientList(ctx)
	if err != nil {
		return err
	}
	script, err := BuildNFTScript(s.cfg, value, clients, LocalAddresses())
	if err != nil {
		return err
	}
	if err := s.firewall.Apply(ctx, script, s.cfg); err != nil {
		return err
	}
	return markApplied(value)
}

func (s *RoutingService) SyncClients(ctx context.Context) error {
	if _, err := os.Stat(s.cfg.RoutingConfig); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	clients, err := s.clientList(ctx)
	if err != nil {
		return err
	}
	names := map[string]struct{}{}
	for _, client := range clients {
		names[client.Name] = struct{}{}
	}
	err = s.store.UpdateConfig(func(value *model.RoutingConfig) error {
		for i := range value.Rules {
			if value.Rules[i].Scope != "clients" {
				continue
			}
			kept := value.Rules[i].Clients[:0]
			for _, name := range value.Rules[i].Clients {
				if _, ok := names[name]; ok {
					kept = append(kept, name)
				}
			}
			value.Rules[i].Clients = kept
			if len(kept) == 0 {
				value.Rules[i].Enabled = false
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	value, _ := s.store.LoadConfig()
	if value.Enabled {
		return s.Apply(ctx)
	}
	return nil
}

func (s *RoutingService) clientList(ctx context.Context) ([]model.Client, error) {
	if s.clients == nil {
		return nil, errors.New("источник AWG-клиентов недоступен")
	}
	return s.clients(ctx)
}

func (s *RoutingService) checkXrayValue(ctx context.Context, warp WarpSecrets) error {
	value, err := BuildXrayConfig(s.cfg, warp)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(value)
	tmp, err := os.CreateTemp("", "awgpanel-xray-*.json")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, s.cfg.XrayBinary, "run", "-test", "-config", path)
	command.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+s.cfg.XrayAssets)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("Xray config check: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *RoutingService) startRollback(ctx context.Context) error {
	_ = systemctlIgnoreMissing(ctx, "stop", "awgpanel-routing-rollback.timer")
	_ = systemctlIgnoreMissing(ctx, "stop", "awgpanel-routing-rollback.service")
	return runCombined(ctx, "systemd-run", "--collect", "--unit=awgpanel-routing-rollback", "--on-active=120s", "--property=Type=oneshot", "/usr/local/bin/awgpanel", "routing", "emergency-disable", "--yes")
}

func needsWarp(value model.RoutingConfig) bool {
	for _, rule := range value.Rules {
		if rule.Enabled && rule.Outbound == "warp" {
			return true
		}
	}
	return true
}

func portAvailable(host string, port int, udp bool) error {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	listener.Close()
	if udp {
		packet, err := net.ListenPacket("udp", address)
		if err != nil {
			return err
		}
		packet.Close()
	}
	return nil
}

func policyConflicts(ctx context.Context, cfg config.Config, enabled bool) []string {
	var failures []string
	priority := strconv.Itoa(cfg.RouteTable) + ":"
	for _, family := range [][]string{{}, {"-6"}} {
		args := append(append([]string{}, family...), "-o", "rule", "show")
		if output, err := exec.CommandContext(ctx, "ip", args...).Output(); err == nil {
			for _, line := range strings.Split(string(output), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, priority) && (!strings.Contains(line, fmt.Sprintf("fwmark 0x%x", cfg.FWMark)) || !strings.Contains(line, "lookup "+strconv.Itoa(cfg.RouteTable))) {
					failures = append(failures, "priority policy rule "+strconv.Itoa(cfg.RouteTable)+" уже занята")
				}
			}
		}
		routeArgs := append(append([]string{}, family...), "route", "show", "table", strconv.Itoa(cfg.RouteTable))
		if output, err := exec.CommandContext(ctx, "ip", routeArgs...).Output(); err == nil && strings.TrimSpace(string(output)) != "" {
			owned := enabled
			for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
				if !strings.HasPrefix(strings.TrimSpace(line), "local default dev lo") {
					owned = false
				}
			}
			if !owned {
				failures = append(failures, "routing table "+strconv.Itoa(cfg.RouteTable)+" уже используется")
			}
		}
	}
	return failures
}

func waitWarpHealth(ctx context.Context, cfg config.Config, warp WarpSecrets) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := CheckWarpHealth(ctx, cfg, warp); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("WARP health-check: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func unitActive(ctx context.Context, unit string) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
}
func nftTableExists(ctx context.Context) bool {
	return exec.CommandContext(ctx, "nft", "list", "table", "inet", "awgpanel").Run() == nil
}
func nftTableOwned(ctx context.Context) bool {
	output, err := exec.CommandContext(ctx, "nft", "list", "table", "inet", "awgpanel").Output()
	return err == nil && strings.Contains(string(output), "chain dns_redirect") && strings.Contains(string(output), "chain route_warp")
}
func systemctl(ctx context.Context, action, unit string) error {
	return runCombined(ctx, "systemctl", action, unit)
}
func systemctlIgnoreMissing(ctx context.Context, action, unit string) error {
	err := systemctl(ctx, action, unit)
	message := ""
	if err != nil {
		message = strings.ToLower(err.Error())
	}
	if err != nil && (strings.Contains(message, "not loaded") || strings.Contains(message, "not found")) {
		return nil
	}
	return err
}
func runCombined(ctx context.Context, binary string, args ...string) error {
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func routingDigest(value model.RoutingConfig) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func markApplied(value model.RoutingConfig) error {
	if err := os.MkdirAll(filepath.Dir(appliedDigestPath), 0700); err != nil {
		return err
	}
	return os.WriteFile(appliedDigestPath, []byte(routingDigest(value)+"\n"), 0600)
}

func isApplied(value model.RoutingConfig) bool {
	b, err := os.ReadFile(appliedDigestPath)
	return err == nil && strings.TrimSpace(string(b)) == routingDigest(value)
}

func (s *RoutingService) acquireNetworkLock(nonblocking bool) (func(), error) {
	if err := os.MkdirAll(s.cfg.RoutingDir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(s.networkLockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	operation := syscall.LOCK_EX
	if nonblocking {
		operation |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(lock.Fd()), operation); err != nil {
		lock.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }, nil
}
