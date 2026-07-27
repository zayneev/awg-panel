package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
	"github.com/zayneev/awg-panel/internal/routing"
	"github.com/zayneev/awg-panel/internal/validate"
)

const maxArtifactSize = 16 << 20

var supportedManageMinors = []string{"5.20", "5.21"}

type Service struct {
	cfg      config.Config
	mutateMu sync.Mutex
	cacheMu  sync.Mutex
	cacheAt  time.Time
	cache    []model.Client
	routing  *routing.RoutingService
}

type CommandError struct {
	ExitCode int
	Stderr   string
	Err      error
}

func (e *CommandError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return msg
}

func NewService(cfg config.Config) *Service {
	service := &Service{cfg: cfg}
	service.routing = routing.NewService(cfg, service.Clients)
	return service
}

type upstreamListClient struct {
	Name       string `json:"name"`
	IP         string `json:"ip"`
	ClientIPv6 string `json:"client_ipv6"`
	StatusCode string `json:"status_code"`
}

type upstreamStatsClient struct {
	Name          string `json:"name"`
	RX            uint64 `json:"rx"`
	TX            uint64 `json:"tx"`
	LastHandshake int64  `json:"last_handshake"`
	StatusCode    string `json:"status_code"`
}

func (s *Service) Compatibility() model.Compatibility {
	result := model.Compatibility{SupportedMinors: append([]string(nil), supportedManageMinors...)}
	manage, err := scriptVersion(s.cfg.ManageScript, "SCRIPT_VERSION")
	if err != nil {
		result.Message = err.Error()
		return result
	}
	common, err := scriptVersion(s.cfg.CommonScript, "AWG_COMMON_VERSION")
	if err != nil {
		result.ManageVersion = manage
		result.Message = err.Error()
		return result
	}
	result.ManageVersion = manage
	result.CommonVersion = common
	manageMinor, commonMinor := minor(manage), minor(common)
	if manageMinor != commonMinor {
		result.Message = "версии manage и awg_common несовместимы"
		return result
	}
	if !isSupportedManageMinor(manageMinor) {
		result.Message = fmt.Sprintf("поддерживаются версии %s; найдены manage=%s, common=%s", supportedManageVersions(), manage, common)
		return result
	}
	result.OK = true
	return result
}

func isSupportedManageMinor(value string) bool {
	for _, supported := range supportedManageMinors {
		if value == supported {
			return true
		}
	}
	return false
}

func supportedManageVersions() string {
	versions := make([]string, 0, len(supportedManageMinors))
	for _, supported := range supportedManageMinors {
		versions = append(versions, supported+".x")
	}
	return strings.Join(versions, ", ")
}

var versionPatterns = map[string]*regexp.Regexp{
	"SCRIPT_VERSION":     regexp.MustCompile(`(?m)^SCRIPT_VERSION=["']([0-9]+\.[0-9]+\.[^"']+)["']`),
	"AWG_COMMON_VERSION": regexp.MustCompile(`(?m)^AWG_COMMON_VERSION=["']([0-9]+\.[0-9]+\.[^"']+)["']`),
}

func scriptVersion(path, variable string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("не удалось прочитать %s: %w", path, err)
	}
	re := versionPatterns[variable]
	if re == nil {
		return "", errors.New("неизвестная переменная версии")
	}
	m := re.FindSubmatch(b)
	if len(m) != 2 {
		return "", fmt.Errorf("в %s не найдена %s", path, variable)
	}
	return string(m[1]), nil
}

func minor(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

func (s *Service) requireCompatible() error {
	c := s.Compatibility()
	if !c.OK {
		return errors.New(c.Message)
	}
	for _, path := range []string{s.cfg.AWGDir, s.cfg.ServerConfig} {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("не найден обязательный путь %s: %w", path, err)
		}
	}
	return nil
}

func (s *Service) manageArgs(args ...string) []string {
	prefix := []string{s.cfg.ManageScript, "--no-color", "--conf-dir=" + s.cfg.AWGDir, "--server-conf=" + s.cfg.ServerConfig}
	return append(prefix, args...)
}

func (s *Service) runManage(ctx context.Context, args ...string) ([]byte, error) {
	if err := s.requireCompatible(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmdArgs := s.manageArgs(args...)
	cmd := exec.CommandContext(ctx, "/bin/bash", cmdArgs...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=/root", "AWG_YES=1"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("таймаут команды manage: %w", ctx.Err())
	}
	if err != nil {
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}
		return nil, &CommandError{ExitCode: exitCode, Stderr: sanitizeDiagnostic(stderr.String()), Err: err}
	}
	return stdout.Bytes(), nil
}

func sanitizeDiagnostic(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 4096 {
		v = v[len(v)-4096:]
	}
	return v
}

func (s *Service) Clients(ctx context.Context) ([]model.Client, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if time.Since(s.cacheAt) < 3*time.Second {
		return append([]model.Client(nil), s.cache...), nil
	}
	listOut, err := s.runManage(ctx, "list", "--json")
	if err != nil {
		return nil, err
	}
	statsOut, statsErr := s.runManage(ctx, "stats", "--json")
	var listed []upstreamListClient
	if err := json.Unmarshal(listOut, &listed); err != nil {
		return nil, fmt.Errorf("некорректный JSON list: %w", err)
	}
	stats := map[string]upstreamStatsClient{}
	if statsErr == nil {
		var values []upstreamStatsClient
		if err := json.Unmarshal(statsOut, &values); err == nil {
			for _, value := range values {
				stats[value.Name] = value
			}
		}
	}
	clients := make([]model.Client, 0, len(listed))
	for _, item := range listed {
		if validate.ClientName(item.Name) != nil {
			continue
		}
		client := model.Client{Name: item.Name, IP: item.IP, ClientIPv6: item.ClientIPv6, StatusCode: item.StatusCode, ExpiryState: "permanent"}
		if stat, ok := stats[item.Name]; ok {
			client.RXBytes, client.TXBytes = stat.RX, stat.TX
			if stat.StatusCode != "" {
				client.StatusCode = stat.StatusCode
			}
			if stat.LastHandshake > 0 {
				t := time.Unix(stat.LastHandshake, 0).UTC()
				client.LastHandshake = &t
			}
		}
		s.addExpiry(&client)
		client.Artifacts = s.artifactAvailability(item.Name)
		clients = append(clients, client)
	}
	sort.Slice(clients, func(i, j int) bool { return clients[i].Name < clients[j].Name })
	s.cache = append([]model.Client(nil), clients...)
	s.cacheAt = time.Now()
	return clients, nil
}

func (s *Service) invalidateClients() {
	s.cacheMu.Lock()
	s.cacheAt = time.Time{}
	s.cache = nil
	s.cacheMu.Unlock()
}

func (s *Service) addExpiry(client *model.Client) {
	path := filepath.Join(s.cfg.AWGDir, "expiry", client.Name)
	b, err := readRegularFile(path, 64)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		client.ExpiryState = "corrupt"
		return
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n <= 0 {
		client.ExpiryState = "corrupt"
		return
	}
	t := time.Unix(n, 0).UTC()
	client.ExpiresAt = &t
	if time.Now().After(t) {
		client.ExpiryState = "expired"
	} else {
		client.ExpiryState = "scheduled"
	}
}

func (s *Service) artifactAvailability(name string) model.ArtifactAvailability {
	return model.ArtifactAvailability{
		Config:   regularFile(filepath.Join(s.cfg.AWGDir, name+".conf")),
		QR:       regularFile(filepath.Join(s.cfg.AWGDir, name+".png")),
		VPNURI:   regularFile(filepath.Join(s.cfg.AWGDir, name+".vpnuri")),
		VPNURIQR: regularFile(filepath.Join(s.cfg.AWGDir, name+".vpnuri.png")),
	}
}

func (s *Service) CreateClient(ctx context.Context, req model.CreateClientRequest) error {
	if err := validate.ClientName(req.Name); err != nil {
		return err
	}
	if err := validate.Expiry(req.Expires); err != nil {
		return err
	}
	args := []string{"add", req.Name}
	if req.Expires != "" {
		args = append(args, "--expires="+req.Expires)
	}
	if req.PSK {
		args = append(args, "--psk")
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	_, err := s.runManage(ctx, args...)
	if err == nil {
		s.invalidateClients()
		if routingErr := s.routing.SyncClients(ctx); routingErr != nil {
			return fmt.Errorf("клиент создан, но routing не обновлён: %w", routingErr)
		}
	}
	return err
}

func (s *Service) ModifyClient(ctx context.Context, name string, req model.ModifyClientRequest) error {
	if err := validate.ClientName(name); err != nil {
		return err
	}
	if err := validate.FieldValue(req.Field, req.Value); err != nil {
		return err
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	_, err := s.runManage(ctx, "modify", name, req.Field, req.Value)
	if err == nil {
		s.invalidateClients()
		if routingErr := s.routing.SyncClients(ctx); routingErr != nil {
			return fmt.Errorf("клиент изменён, но routing не обновлён: %w", routingErr)
		}
	}
	return err
}

func (s *Service) DeleteClient(ctx context.Context, name string) error {
	if err := validate.ClientName(name); err != nil {
		return err
	}
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	_, err := s.runManage(ctx, "remove", name, "--yes")
	if err == nil {
		s.invalidateClients()
		if routingErr := s.routing.SyncClients(ctx); routingErr != nil {
			return fmt.Errorf("клиент удалён, но routing не обновлён: %w", routingErr)
		}
	}
	return err
}

func (s *Service) RoutingStatus(ctx context.Context) model.RoutingStatus {
	return s.routing.Status(ctx)
}
func (s *Service) RoutingCheck(ctx context.Context) model.RoutingCheck { return s.routing.Check(ctx) }
func (s *Service) RoutingEnable(ctx context.Context) error             { return s.routing.Enable(ctx) }
func (s *Service) RoutingDisable(ctx context.Context) error            { return s.routing.Disable(ctx) }
func (s *Service) RoutingApply(ctx context.Context) error              { return s.routing.Apply(ctx) }
func (s *Service) RoutingEmergencyDisable(ctx context.Context) error {
	return s.routing.EmergencyDisable(ctx)
}
func (s *Service) RoutingRemoveIntercept(ctx context.Context) error {
	return s.routing.RemoveIntercept(ctx)
}
func (s *Service) RoutingRules() ([]model.RoutingRule, error)  { return s.routing.Rules() }
func (s *Service) RoutingRuleAdd(rule model.RoutingRule) error { return s.routing.AddRule(rule) }
func (s *Service) RoutingRuleSet(rule model.RoutingRule) error { return s.routing.SetRule(rule) }
func (s *Service) RoutingRuleToggle(id string, enabled bool) error {
	return s.routing.ToggleRule(id, enabled)
}
func (s *Service) RoutingRuleDelete(id string) error { return s.routing.DeleteRule(id) }
func (s *Service) RoutingWarpRegister(ctx context.Context, accept bool) error {
	return s.routing.WarpRegister(ctx, accept)
}
func (s *Service) RoutingWarpImport(path string) error { return s.routing.WarpImport(path) }
func (s *Service) RoutingWarpTest(ctx context.Context) (model.WarpStatus, error) {
	return s.routing.WarpTest(ctx)
}
func (s *Service) RoutingWarpForget(ctx context.Context) error { return s.routing.WarpForget(ctx) }
func (s *Service) RoutingRunDNS(ctx context.Context) error     { return s.routing.RunDNS(ctx) }
func (s *Service) RoutingRecover(ctx context.Context) error    { return s.routing.Recover(ctx) }

func (s *Service) Restart(ctx context.Context) error {
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	_, err := s.runManage(ctx, "restart", "--yes")
	return err
}

type Artifact struct {
	Name        string
	ContentType string
	Data        []byte
}

type FileArtifact struct {
	Name        string
	Path        string
	ContentType string
	File        *os.File
	Size        int64
}

var artifactTypes = map[string]struct{ suffix, contentType string }{
	"config":     {".conf", "application/x-wireguard-profile"},
	"qr":         {".png", "image/png"},
	"vpn-uri":    {".vpnuri", "text/plain; charset=utf-8"},
	"vpn-uri-qr": {".vpnuri.png", "image/png"},
}

func (s *Service) Artifact(name, kind string) (Artifact, error) {
	if err := validate.ClientName(name); err != nil {
		return Artifact{}, err
	}
	typeInfo, ok := artifactTypes[kind]
	if !ok {
		return Artifact{}, errors.New("неизвестный тип артефакта")
	}
	filename := name + typeInfo.suffix
	data, err := readRegularFile(filepath.Join(s.cfg.AWGDir, filename), maxArtifactSize)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Name: filename, ContentType: typeInfo.contentType, Data: data}, nil
}

func (s *Service) Backup(ctx context.Context) (FileArtifact, error) {
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()
	started := time.Now().Add(-time.Second)
	if _, err := s.runManage(ctx, "backup"); err != nil {
		return FileArtifact{}, err
	}
	dir := filepath.Join(s.cfg.AWGDir, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return FileArtifact{}, err
	}
	type candidate struct {
		path, name string
		mod        time.Time
	}
	var files []candidate
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.HasPrefix(entry.Name(), "awg_backup_") || !strings.HasSuffix(entry.Name(), ".tar.gz") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().Before(started) {
			continue
		}
		files = append(files, candidate{filepath.Join(dir, entry.Name()), entry.Name(), info.ModTime()})
	}
	if len(files) == 0 {
		return FileArtifact{}, errors.New("команда завершилась, но новый backup не найден")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	file, size, err := openRegularFile(files[0].path, 512<<20)
	if err != nil {
		return FileArtifact{}, err
	}
	return FileArtifact{Name: files[0].name, Path: files[0].path, ContentType: "application/gzip", File: file, Size: size}, nil
}

func (s *Service) Status(ctx context.Context) model.ServerStatus {
	result := model.ServerStatus{Compatibility: s.Compatibility(), CheckedAt: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/systemctl", "is-active", "--quiet", "awg-quick@awg0")
	result.ServiceActive = cmd.Run() == nil
	if result.ServiceActive {
		result.UptimeSeconds = awgServiceUptime(ctx)
	}
	if result.Compatibility.OK {
		if clients, err := s.Clients(ctx); err == nil {
			result.TotalClients = len(clients)
			for _, c := range clients {
				result.RXBytes += c.RXBytes
				result.TXBytes += c.TXBytes
				switch c.StatusCode {
				case "active":
					result.ActiveClients++
				case "recent":
					result.RecentClients++
				}
			}
		}
	}
	result.Healthy = result.ServiceActive && result.Compatibility.OK
	return result
}

func awgServiceUptime(ctx context.Context) uint64 {
	cmd := exec.CommandContext(ctx, "/bin/systemctl", "show", "awg-quick@awg0", "--property=ActiveEnterTimestampMonotonic", "--value")
	active, err := cmd.Output()
	if err != nil {
		return 0
	}
	procUptime, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	value, err := uptimeFromMonotonic(string(active), string(procUptime))
	if err != nil {
		return 0
	}
	return value
}

func uptimeFromMonotonic(activeValue, procUptime string) (uint64, error) {
	activeMicros, err := strconv.ParseUint(strings.TrimSpace(activeValue), 10, 64)
	if err != nil || activeMicros == 0 {
		return 0, errors.New("некорректное время запуска systemd")
	}
	fields := strings.Fields(procUptime)
	if len(fields) == 0 {
		return 0, errors.New("пустой /proc/uptime")
	}
	bootSeconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || bootSeconds < 0 {
		return 0, errors.New("некорректный /proc/uptime")
	}
	nowMicros := uint64(bootSeconds * 1_000_000)
	if activeMicros > nowMicros {
		return 0, errors.New("время запуска сервиса находится в будущем")
	}
	return (nowMicros - activeMicros) / 1_000_000, nil
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func readRegularFile(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("разрешены только обычные файлы")
	}
	if info.Size() > max {
		return nil, errors.New("файл превышает допустимый размер")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max+1))
}

func openRegularFile(path string, max int64) (*os.File, int64, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if !before.Mode().IsRegular() || before.Size() > max {
		return nil, 0, errors.New("недопустимый файл или размер")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		file.Close()
		return nil, 0, errors.New("файл изменился во время открытия")
	}
	return file, after.Size(), nil
}
