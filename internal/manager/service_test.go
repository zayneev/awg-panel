package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/model"
)

func testService(t *testing.T) (*Service, config.Config, string) {
	t.Helper()
	root := t.TempDir()
	awgDir := filepath.Join(root, "awg")
	if err := os.MkdirAll(filepath.Join(awgDir, "expiry"), 0700); err != nil {
		t.Fatal(err)
	}
	serverConf := filepath.Join(root, "awg0.conf")
	common := filepath.Join(awgDir, "awg_common.sh")
	manage := filepath.Join(awgDir, "manage_amneziawg.sh")
	logPath := filepath.Join(root, "commands.log")
	mustWrite(t, serverConf, "[Interface]\n")
	mustWrite(t, common, "AWG_COMMON_VERSION=\"5.20.1\"\n")
	script := fmt.Sprintf(`#!/bin/bash
SCRIPT_VERSION="5.20.1"
command_name=""
for arg in "$@"; do
  case "$arg" in list|stats|add|remove|modify|restart|backup) command_name="$arg"; break;; esac
done
case "$command_name" in
  list) echo '[{"name":"phone","ip":"10.9.9.2","client_ipv6":"fd00::2","status_code":"recent"}]' ;;
  stats) echo '[{"name":"phone","ip":"10.9.9.2","rx":1024,"tx":2048,"last_handshake":1710312180,"status_code":"active"}]' ;;
  *) printf '%%s\n' "$*" >> %q ;;
esac
`, logPath)
	mustWrite(t, manage, script)
	mustWrite(t, filepath.Join(awgDir, "phone.conf"), "[Interface]\n")
	mustWrite(t, filepath.Join(awgDir, "phone.png"), "PNG")
	mustWrite(t, filepath.Join(awgDir, "expiry", "phone"), fmt.Sprint(time.Now().Add(time.Hour).Unix()))
	cfg := config.Default()
	cfg.AWGDir, cfg.ManageScript, cfg.CommonScript, cfg.ServerConfig = awgDir, manage, common, serverConf
	return NewService(cfg), cfg, logPath
}

func mustWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClientsMergeStatsExpiryAndArtifacts(t *testing.T) {
	svc, _, _ := testService(t)
	clients, err := svc.Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 {
		t.Fatalf("got %d clients", len(clients))
	}
	c := clients[0]
	if c.Name != "phone" || c.StatusCode != "active" || c.RXBytes != 1024 || c.ExpiresAt == nil || !c.Artifacts.Config || !c.Artifacts.QR {
		t.Fatalf("unexpected merged client: %+v", c)
	}
}

func TestCreateUsesArgvAndAllowlist(t *testing.T) {
	svc, _, logPath := testService(t)
	if err := svc.CreateClient(context.Background(), model.CreateClientRequest{Name: "tablet_1", Expires: "7d", PSK: true}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	line := string(b)
	for _, expected := range []string{"add tablet_1", "--expires=7d", "--psk"} {
		if !strings.Contains(line, expected) {
			t.Fatalf("command %q does not include %q", line, expected)
		}
	}
	if err := svc.CreateClient(context.Background(), model.CreateClientRequest{Name: "../root"}); err == nil {
		t.Fatal("path traversal client accepted")
	}
}

func TestArtifactRejectsSymlink(t *testing.T) {
	svc, cfg, _ := testService(t)
	target := filepath.Join(cfg.AWGDir, "target.conf")
	mustWrite(t, target, "secret")
	if err := os.Symlink(target, filepath.Join(cfg.AWGDir, "evil.conf")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := svc.Artifact("evil", "config"); err == nil {
		t.Fatal("symlink artifact accepted")
	}
}

func TestCompatibilityMatrix(t *testing.T) {
	tests := []struct {
		name, manage, common string
		wantOK               bool
		message              string
	}{
		{name: "5.20", manage: "5.20.1", common: "5.20.9", wantOK: true},
		{name: "5.21", manage: "5.21.2", common: "5.21.0", wantOK: true},
		{name: "future minor", manage: "5.22.0", common: "5.22.1", message: "5.20.x, 5.21.x"},
		{name: "mixed minors", manage: "5.20.1", common: "5.21.2", message: "несовместимы"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, cfg, _ := testService(t)
			mustWrite(t, cfg.ManageScript, fmt.Sprintf("SCRIPT_VERSION=\"%s\"\n", tt.manage))
			mustWrite(t, cfg.CommonScript, fmt.Sprintf("AWG_COMMON_VERSION=\"%s\"\n", tt.common))
			compat := svc.Compatibility()
			if compat.OK != tt.wantOK || (tt.message != "" && !strings.Contains(compat.Message, tt.message)) {
				t.Fatalf("unexpected compatibility: %+v", compat)
			}
			if strings.Join(compat.SupportedMinors, ",") != "5.20,5.21" {
				t.Fatalf("unexpected supported minors: %v", compat.SupportedMinors)
			}
		})
	}
}

func TestLegacyRequiredMinorDoesNotOverrideReleaseCompatibility(t *testing.T) {
	svc, _, _ := testService(t)
	svc.cfg.RequiredManageMinor = "9.99"
	if compat := svc.Compatibility(); !compat.OK {
		t.Fatalf("legacy config field changed compatibility: %+v", compat)
	}
}

func TestManageContract521(t *testing.T) {
	svc, cfg, logPath := testService(t)
	mustWrite(t, cfg.ManageScript, strings.ReplaceAll(mustRead(t, cfg.ManageScript), "5.20.1", "5.21.2"))
	mustWrite(t, cfg.CommonScript, "AWG_COMMON_VERSION=\"5.21.2\"\n")

	listOut, err := svc.runManage(context.Background(), "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var listed []upstreamListClient
	if err := json.Unmarshal(listOut, &listed); err != nil || len(listed) != 1 || listed[0].ClientIPv6 != "fd00::2" {
		t.Fatalf("unexpected list contract: %s (%v)", listOut, err)
	}
	statsOut, err := svc.runManage(context.Background(), "stats", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var stats []upstreamStatsClient
	if err := json.Unmarshal(statsOut, &stats); err != nil || len(stats) != 1 || stats[0].LastHandshake != 1710312180 {
		t.Fatalf("unexpected stats contract: %s (%v)", statsOut, err)
	}

	commands := [][]string{
		{"add", "tablet", "--expires=7d", "--psk"},
		{"modify", "phone", "DNS", "1.1.1.1"},
		{"remove", "phone", "--yes"},
		{"backup"},
		{"restart", "--yes"},
	}
	for _, args := range commands {
		if _, err := svc.runManage(context.Background(), args...); err != nil {
			t.Fatalf("%s contract failed: %v", args[0], err)
		}
	}
	log := mustRead(t, logPath)
	for _, command := range []string{"add tablet", "modify phone", "remove phone", "backup", "restart"} {
		if !strings.Contains(log, command) {
			t.Fatalf("command log does not contain %q: %s", command, log)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUptimeFromMonotonic(t *testing.T) {
	got, err := uptimeFromMonotonic("2500000\n", "12.75 4.10\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != 10 {
		t.Fatalf("got uptime %d, want 10", got)
	}
	if _, err := uptimeFromMonotonic("bad", "12.75 4.10"); err == nil {
		t.Fatal("invalid systemd timestamp accepted")
	}
}
