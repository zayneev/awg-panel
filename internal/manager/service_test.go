package manager

import (
	"context"
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

func TestCompatibilityFailsClosed(t *testing.T) {
	svc, cfg, _ := testService(t)
	mustWrite(t, cfg.CommonScript, "AWG_COMMON_VERSION=\"5.21.0\"\n")
	compat := svc.Compatibility()
	if compat.OK || !strings.Contains(compat.Message, "5.20") {
		t.Fatalf("unexpected compatibility: %+v", compat)
	}
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
