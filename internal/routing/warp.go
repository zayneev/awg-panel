package routing

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
	"golang.org/x/net/proxy"
)

const (
	warpAPI           = "https://api.cloudflareclient.com/v0a4005/reg"
	warpClientVersion = "a-6.30-3596"
)

func ImportWGQuick(path string) (WarpSecrets, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return WarpSecrets{}, err
	}
	value, err := ParseWGQuick(string(b))
	if err != nil {
		return WarpSecrets{}, err
	}
	if err := rejectReusedXUIKey(value.PrivateKey, []string{"/etc/x-ui/x-ui.db", "/usr/local/x-ui/x-ui.db", "/usr/local/x-ui/bin/config.json", "/etc/xray/config.json"}); err != nil {
		return WarpSecrets{}, err
	}
	return value, nil
}

func ParseWGQuick(value string) (WarpSecrets, error) {
	result := WarpSecrets{Version: 1, Source: "imported", MTU: 1420}
	section := ""
	var err error
	scanner := bufio.NewScanner(strings.NewReader(value))
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return WarpSecrets{}, fmt.Errorf("некорректная строка wg-quick: %q", line)
		}
		key, item := strings.ToLower(strings.TrimSpace(parts[0])), strings.TrimSpace(parts[1])
		switch section + "." + key {
		case "interface.privatekey":
			result.PrivateKey = item
		case "interface.address":
			result.Addresses = splitCSV(item)
		case "interface.dns":
			result.DNS = splitCSV(item)
		case "interface.mtu":
			result.MTU, err = strconv.Atoi(item)
			if err != nil {
				return WarpSecrets{}, errors.New("некорректный MTU")
			}
		case "peer.publickey":
			result.PeerKey = item
		case "peer.presharedkey":
			result.Preshared = item
		case "peer.endpoint":
			result.Endpoint = item
		case "peer.allowedips":
			result.AllowedIPs = splitCSV(item)
		}
	}
	if err := scanner.Err(); err != nil {
		return WarpSecrets{}, err
	}
	if len(result.AllowedIPs) == 0 {
		result.AllowedIPs = []string{"0.0.0.0/0", "::/0"}
	}
	result.HealthUser, result.HealthPass, err = newHealthCredentials()
	if err != nil {
		return WarpSecrets{}, err
	}
	if err := validateWireGuardKeys(result); err != nil {
		return WarpSecrets{}, err
	}
	return result, ValidateWarp(result)
}

func RegisterWarp(ctx context.Context, acceptTerms bool) (WarpSecrets, error) {
	return registerWarp(ctx, acceptTerms, warpAPI, &http.Client{Timeout: 15 * time.Second})
}

func registerWarp(ctx context.Context, acceptTerms bool, apiURL string, client *http.Client) (WarpSecrets, error) {
	if !acceptTerms {
		return WarpSecrets{}, errors.New("регистрация требует явного подтверждения условий Cloudflare WARP")
	}
	privateBytes := make([]byte, 32)
	if _, err := rand.Read(privateBytes); err != nil {
		return WarpSecrets{}, err
	}
	privateBytes[0] &= 248
	privateBytes[31] &= 127
	privateBytes[31] |= 64
	privateKey, err := ecdh.X25519().NewPrivateKey(privateBytes)
	if err != nil {
		return WarpSecrets{}, err
	}
	public := base64.StdEncoding.EncodeToString(privateKey.PublicKey().Bytes())
	payload, _ := json.Marshal(map[string]any{
		"key": public, "tos": time.Now().UTC().Format(time.RFC3339Nano),
		"type": "PC", "model": "awgpanel", "locale": "en_US",
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return WarpSecrets{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "okhttp/3.12.1")
	request.Header.Set("CF-Client-Version", warpClientVersion)
	response, err := client.Do(request)
	if err != nil {
		return WarpSecrets{}, fmt.Errorf("WARP registration: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return WarpSecrets{}, fmt.Errorf("WARP registration: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		ID     string `json:"id"`
		Token  string `json:"token"`
		Config struct {
			ClientID  string `json:"client_id"`
			Interface struct {
				Addresses struct {
					V4 string `json:"v4"`
					V6 string `json:"v6"`
				} `json:"addresses"`
			} `json:"interface"`
			Peers []struct {
				PublicKey string `json:"public_key"`
				Endpoint  struct {
					V4   string `json:"v4"`
					V6   string `json:"v6"`
					Host string `json:"host"`
				} `json:"endpoint"`
			} `json:"peers"`
		} `json:"config"`
		Account struct {
			License string `json:"license"`
		} `json:"account"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return WarpSecrets{}, fmt.Errorf("WARP registration response: %w", err)
	}
	if len(envelope.Config.Peers) == 0 {
		return WarpSecrets{}, errors.New("WARP registration не вернула peer")
	}
	endpoint := envelope.Config.Peers[0].Endpoint.Host
	if endpoint == "" {
		endpoint = envelope.Config.Peers[0].Endpoint.V4
	}
	reserved, _ := base64.StdEncoding.DecodeString(envelope.Config.ClientID)
	user, pass, err := newHealthCredentials()
	if err != nil {
		return WarpSecrets{}, err
	}
	result := WarpSecrets{
		Version: 1, Source: "registered", PrivateKey: base64.StdEncoding.EncodeToString(privateBytes), PublicKey: public,
		Addresses: []string{envelope.Config.Interface.Addresses.V4, envelope.Config.Interface.Addresses.V6},
		DNS:       []string{"1.1.1.1", "2606:4700:4700::1111"}, MTU: 1280,
		PeerKey: envelope.Config.Peers[0].PublicKey, Endpoint: endpoint,
		AllowedIPs: []string{"0.0.0.0/0", "::/0"}, Reserved: reserved,
		DeviceID: envelope.ID, AccessToken: envelope.Token, License: envelope.Account.License,
		HealthUser: user, HealthPass: pass,
	}
	result.Addresses, err = normalizeWarpAddresses(result.Addresses)
	if err != nil {
		return WarpSecrets{}, err
	}
	if err := validateWireGuardKeys(result); err != nil {
		return WarpSecrets{}, err
	}
	return result, ValidateWarp(result)
}

func normalizeWarpAddresses(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			result = append(result, prefix.String())
			continue
		}
		address, err := netip.ParseAddr(value)
		if err != nil {
			return nil, fmt.Errorf("некорректный WARP address %q", value)
		}
		result = append(result, netip.PrefixFrom(address, address.BitLen()).String())
	}
	return result, nil
}

func validateWireGuardKeys(value WarpSecrets) error {
	for label, encoded := range map[string]string{"private key": value.PrivateKey, "peer public key": value.PeerKey} {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("некорректный WireGuard %s", label)
		}
	}
	for _, address := range value.Addresses {
		if _, err := netip.ParsePrefix(address); err != nil {
			return fmt.Errorf("некорректный WARP address %q", address)
		}
	}
	if _, _, err := net.SplitHostPort(value.Endpoint); err != nil {
		return fmt.Errorf("некорректный WARP endpoint %q", value.Endpoint)
	}
	return nil
}

func newHealthCredentials() (string, string, error) {
	b := make([]byte, 36)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:12]), base64.RawURLEncoding.EncodeToString(b[12:]), nil
}

func splitCSV(value string) []string {
	return compactStrings(strings.Split(value, ","))
}

func compactStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func rejectReusedXUIKey(privateKey string, paths []string) error {
	needle := []byte(privateKey)
	for _, path := range paths {
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			continue
		}
		if err != nil {
			return fmt.Errorf("проверить конфигурацию 3x-ui: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 128<<20))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		if bytes.Contains(data, needle) {
			return errors.New("импорт отклонён: private key уже встречается в конфигурации 3x-ui/Xray")
		}
	}
	return nil
}

func BuildXrayConfig(cfg config.Config, warp WarpSecrets) (map[string]any, error) {
	if err := ValidateWarp(warp); err != nil {
		return nil, err
	}
	peer := map[string]any{"publicKey": warp.PeerKey, "endpoint": warp.Endpoint, "allowedIPs": warp.AllowedIPs}
	if warp.Preshared != "" {
		peer["preSharedKey"] = warp.Preshared
	}
	settings := map[string]any{
		"secretKey": warp.PrivateKey, "address": warp.Addresses, "peers": []any{peer},
		"mtu": warp.MTU, "noKernelTun": true, "domainStrategy": "ForceIP",
	}
	if len(warp.Reserved) == 3 {
		settings["reserved"] = warp.Reserved
	}
	return map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			map[string]any{"tag": "awgpanel-tproxy", "listen": "0.0.0.0", "port": cfg.TProxyPort, "protocol": "dokodemo-door", "settings": map[string]any{"network": "tcp,udp", "followRedirect": true}, "streamSettings": map[string]any{"sockopt": map[string]any{"tproxy": "tproxy"}}},
			map[string]any{"tag": "awgpanel-health", "listen": "127.0.0.1", "port": cfg.HealthPort, "protocol": "socks", "settings": map[string]any{"auth": "password", "accounts": []any{map[string]any{"user": warp.HealthUser, "pass": warp.HealthPass}}, "udp": false}},
		},
		"outbounds": []any{map[string]any{"tag": "warp", "protocol": "wireguard", "settings": settings}},
		"routing":   map[string]any{"domainStrategy": "AsIs", "rules": []any{map[string]any{"type": "field", "inboundTag": []string{"awgpanel-tproxy", "awgpanel-health"}, "outboundTag": "warp"}}},
	}, nil
}

func WriteXrayConfig(cfg config.Config, warp WarpSecrets) error {
	value, err := BuildXrayConfig(cfg, warp)
	if err != nil {
		return err
	}
	return atomicWriteJSON(cfg.XrayConfig, value, 0600)
}

func CheckXrayConfig(ctx context.Context, cfg config.Config) error {
	command := exec.CommandContext(ctx, cfg.XrayBinary, "run", "-test", "-config", cfg.XrayConfig)
	command.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+cfg.XrayAssets)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("Xray config check: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func CheckWarpHealth(ctx context.Context, cfg config.Config, warp WarpSecrets) (model.WarpStatus, error) {
	checked := time.Now()
	status := model.WarpStatus{Configured: true, Source: warp.Source, Endpoint: warp.Endpoint, Addresses: append([]string{}, warp.Addresses...), CheckedAt: &checked}
	dialer, err := proxy.SOCKS5("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.HealthPort)), &proxy.Auth{User: warp.HealthUser, Password: warp.HealthPass}, proxy.Direct)
	if err != nil {
		status.Message = err.Error()
		return status, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.Dial(network, address)
	}}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.cloudflare.com/cdn-cgi/trace", nil)
	response, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		status.Message = err.Error()
		return status, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return status, fmt.Errorf("Cloudflare trace: HTTP %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(io.LimitReader(response.Body, 64<<10))
	trace := map[string]string{}
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "=", 2)
		if len(parts) == 2 {
			trace[parts[0]] = parts[1]
		}
	}
	status.EgressIP, status.Colo = trace["ip"], trace["colo"]
	status.Healthy = trace["warp"] == "on" || trace["warp"] == "plus"
	if !status.Healthy {
		status.Message = "Cloudflare trace не подтвердил WARP"
		return status, errors.New(status.Message)
	}
	status.Message = "WARP работает"
	return status, nil
}
