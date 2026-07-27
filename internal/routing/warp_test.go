package routing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
)

func TestWGQuickParserAndXrayNoKernelTun(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	input := "[Interface]\nPrivateKey = " + key + "\nAddress = 172.16.0.2/32, 2606:4700:110:1::2/128\nDNS = 1.1.1.1\nMTU = 1280\n\n[Peer]\nPublicKey = " + key + "\nEndpoint = engage.cloudflareclient.com:2408\nAllowedIPs = 0.0.0.0/0, ::/0\n"
	warp, err := ParseWGQuick(input)
	if err != nil {
		t.Fatal(err)
	}
	value, err := BuildXrayConfig(config.Default(), warp)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(value)
	text := string(b)
	if !strings.Contains(text, `"noKernelTun":true`) || !strings.Contains(text, `"listen":"127.0.0.1"`) || !strings.Contains(text, `"auth":"password"`) {
		t.Fatalf("unsafe Xray config: %s", text)
	}
}

func TestWGQuickRejectsMalformedSecrets(t *testing.T) {
	if _, err := ParseWGQuick("[Interface]\nPrivateKey = bad\nAddress = 1.1.1.1/32\n[Peer]\nPublicKey = bad\nEndpoint = host:2408\n"); err == nil {
		t.Fatal("invalid keys must fail")
	}
}

func TestRejectsPrivateKeyFoundInXUIStorage(t *testing.T) {
	path := t.TempDir() + "/x-ui.db"
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := os.WriteFile(path, []byte("prefix "+key+" suffix"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rejectReusedXUIKey(key, []string{path}); err == nil {
		t.Fatal("reused private key must be rejected")
	}
}

func TestXrayConfigAgainstPinnedBinary(t *testing.T) {
	binary := os.Getenv("AWGPANEL_XRAY_TEST_BINARY")
	if binary == "" {
		t.Skip("set AWGPANEL_XRAY_TEST_BINARY")
	}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	warp := WarpSecrets{Version: 1, Source: "imported", PrivateKey: key, Addresses: []string{"172.16.0.2/32", "2606:4700:110:1::2/128"}, MTU: 1280, PeerKey: key, Endpoint: "engage.cloudflareclient.com:2408", AllowedIPs: []string{"0.0.0.0/0", "::/0"}, Reserved: []byte{1, 2, 3}, HealthUser: "health", HealthPass: "password"}
	cfg := config.Default()
	cfg.XrayConfig = filepath.Join(t.TempDir(), "xray.json")
	value, err := BuildXrayConfig(cfg, warp)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteJSON(cfg.XrayConfig, value, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "run", "-test", "-config", cfg.XrayConfig)
	command.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+filepath.Dir(binary))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected generated config: %v: %s", err, output)
	}
}

func TestWarpRegistrationAPI(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.Header.Get("CF-Client-Version") != warpClientVersion {
			t.Errorf("unexpected request")
		}
		body := `{"id":"device","token":"token","config":{"client_id":"AQID","interface":{"addresses":{"v4":"172.16.0.2","v6":"2606:4700:110:1::2"}},"peers":[{"public_key":"` + key + `","endpoint":{"host":"engage.cloudflareclient.com:2408"}}]},"account":{"license":"license"}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	warp, err := registerWarp(context.Background(), true, "https://unit.test/reg", client)
	if err != nil {
		t.Fatal(err)
	}
	if warp.Source != "registered" || len(warp.Reserved) != 3 || warp.AccessToken != "token" || warp.HealthPass == "" {
		t.Fatal("unexpected registration response mapping")
	}
	if strings.Join(warp.Addresses, ",") != "172.16.0.2/32,2606:4700:110:1::2/128" {
		t.Fatalf("unexpected normalized addresses: %v", warp.Addresses)
	}
	if _, err := registerWarp(context.Background(), false, "https://unit.test/reg", client); err == nil {
		t.Fatal("terms confirmation must be required")
	}
}

func TestNormalizeWarpAddressesPreservesPrefixes(t *testing.T) {
	got, err := normalizeWarpAddresses([]string{" 172.16.0.2/32 ", "2606:4700:110:1::2/128", ""})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "172.16.0.2/32,2606:4700:110:1::2/128" {
		t.Fatalf("unexpected addresses: %v", got)
	}
	if _, err := normalizeWarpAddresses([]string{"not-an-address"}); err == nil {
		t.Fatal("invalid address must fail")
	}
}

func TestWaitWarpHealthStatusRetriesStartupRace(t *testing.T) {
	attempts := 0
	check := func(context.Context, config.Config, WarpSecrets) (model.WarpStatus, error) {
		attempts++
		if attempts < 3 {
			return model.WarpStatus{}, errors.New("connection refused")
		}
		return model.WarpStatus{Healthy: true, EgressIP: "203.0.113.1"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status, err := waitWarpHealthStatus(ctx, config.Default(), WarpSecrets{}, check)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || !status.Healthy || status.EgressIP != "203.0.113.1" {
		t.Fatalf("unexpected retry result: attempts=%d status=%+v", attempts, status)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
