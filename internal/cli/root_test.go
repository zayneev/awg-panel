package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/zayneev/awg-panel/internal/manager"
	"github.com/zayneev/awg-panel/internal/model"
)

type fakeManager struct {
	clients []model.Client
	files   map[string][]byte
	created model.CreateClientRequest
}

func (f *fakeManager) Status(context.Context) model.ServerStatus {
	return model.ServerStatus{Healthy: true, ServiceActive: true, Compatibility: model.Compatibility{OK: true, ManageVersion: "5.20.1", CommonVersion: "5.20.1"}}
}
func (f *fakeManager) Clients(context.Context) ([]model.Client, error) {
	return append([]model.Client(nil), f.clients...), nil
}
func (f *fakeManager) CreateClient(_ context.Context, request model.CreateClientRequest) error {
	f.created = request
	return nil
}
func (f *fakeManager) ModifyClient(context.Context, string, model.ModifyClientRequest) error {
	return nil
}
func (f *fakeManager) DeleteClient(context.Context, string) error { return nil }
func (f *fakeManager) Restart(context.Context) error              { return nil }
func (f *fakeManager) Artifact(_ string, kind string) (manager.Artifact, error) {
	data, ok := f.files[kind]
	if !ok {
		return manager.Artifact{}, os.ErrNotExist
	}
	return manager.Artifact{Name: "phone.conf", Data: append([]byte(nil), data...)}, nil
}
func (f *fakeManager) Backup(context.Context) (manager.FileArtifact, error) {
	return manager.FileArtifact{}, errors.New("not implemented")
}

func testRoot(m *fakeManager, stdin string, terminal func(any) bool) (*bytes.Buffer, *bytes.Buffer, *cobra.Command) {
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	root := NewRoot("test", Dependencies{
		Factory: func(string) (Manager, error) { return m, nil },
		EUID:    func() int { return 0 },
		Stdin:   strings.NewReader(stdin),
		Stdout:  out,
		Stderr:  stderr,
		IsTerminal: func(value any) bool {
			if terminal == nil {
				return false
			}
			return terminal(value)
		},
	})
	return out, stderr, root
}

func TestConfigWritesExactBytes(t *testing.T) {
	data := []byte("[Interface]\nPrivateKey = secret\n")
	m := &fakeManager{files: map[string][]byte{"config": data}}
	out, _, root := testRoot(m, "", nil)
	root.SetArgs([]string{"clients", "config", "phone"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Fatalf("config changed: %q", out.Bytes())
	}
}

func TestConfigRefusesTerminalOutput(t *testing.T) {
	m := &fakeManager{files: map[string][]byte{"config": []byte("secret")}}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	root := NewRoot("test", Dependencies{
		Factory: func(string) (Manager, error) { return m, nil }, EUID: func() int { return 0 },
		Stdin: strings.NewReader(""), Stdout: stdout, Stderr: stderr,
		IsTerminal: func(value any) bool { return value == stdout },
	})
	root.SetArgs([]string{"clients", "config", "phone"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "приватный ключ") {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatal("secret was written to terminal")
	}
}

func TestAddPassesOptionsAndShowsDeliveryCommands(t *testing.T) {
	m := &fakeManager{}
	out, _, root := testRoot(m, "", nil)
	root.SetArgs([]string{"clients", "add", "phone", "--expires", "7d", "--psk"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if m.created.Name != "phone" || m.created.Expires != "7d" || !m.created.PSK {
		t.Fatalf("unexpected request: %+v", m.created)
	}
	for _, expected := range []string{"clients qr phone --type vpn", "clients uri phone", "clients config phone"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("output does not contain %q: %s", expected, out.String())
		}
	}
}

func TestListJSONIsMachineReadable(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	m := &fakeManager{clients: []model.Client{{Name: "phone", IP: "10.8.1.2", LastHandshake: &now}}}
	out, _, root := testRoot(m, "", nil)
	root.SetArgs([]string{"clients", "list", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"name": "phone"`) || strings.Contains(out.String(), "CLIENT") {
		t.Fatalf("unexpected JSON: %s", out.String())
	}
}

func TestRootRequiresPrivilegesBeforeTUI(t *testing.T) {
	root := NewRoot("test", Dependencies{
		Factory: func(string) (Manager, error) { return &fakeManager{}, nil },
		EUID:    func() int { return 1000 }, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		IsTerminal: func(any) bool { return true },
	})
	root.SetArgs(nil)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type fakeRoutingManager struct {
	*fakeManager
	status       model.RoutingStatus
	rules        []model.RoutingRule
	added        model.RoutingRule
	enableCalled bool
}

func (f *fakeRoutingManager) RoutingStatus(context.Context) model.RoutingStatus { return f.status }
func (f *fakeRoutingManager) RoutingCheck(context.Context) model.RoutingCheck {
	return model.RoutingCheck{OK: true}
}
func (f *fakeRoutingManager) RoutingEnable(context.Context) error           { f.enableCalled = true; return nil }
func (f *fakeRoutingManager) RoutingDisable(context.Context) error          { return nil }
func (f *fakeRoutingManager) RoutingApply(context.Context) error            { return nil }
func (f *fakeRoutingManager) RoutingEmergencyDisable(context.Context) error { return nil }
func (f *fakeRoutingManager) RoutingRemoveIntercept(context.Context) error  { return nil }
func (f *fakeRoutingManager) RoutingRules() ([]model.RoutingRule, error) {
	return append([]model.RoutingRule{}, f.rules...), nil
}
func (f *fakeRoutingManager) RoutingRuleAdd(rule model.RoutingRule) error     { f.added = rule; return nil }
func (f *fakeRoutingManager) RoutingRuleSet(model.RoutingRule) error          { return nil }
func (f *fakeRoutingManager) RoutingRuleToggle(string, bool) error            { return nil }
func (f *fakeRoutingManager) RoutingRuleDelete(string) error                  { return nil }
func (f *fakeRoutingManager) RoutingWarpRegister(context.Context, bool) error { return nil }
func (f *fakeRoutingManager) RoutingWarpImport(string) error                  { return nil }
func (f *fakeRoutingManager) RoutingWarpTest(context.Context) (model.WarpStatus, error) {
	return model.WarpStatus{Healthy: true}, nil
}
func (f *fakeRoutingManager) RoutingWarpForget(context.Context) error { return nil }
func (f *fakeRoutingManager) RoutingRunDNS(context.Context) error     { return nil }
func (f *fakeRoutingManager) RoutingRecover(context.Context) error    { return nil }

func routingTestRoot(m Manager) (*bytes.Buffer, *cobra.Command) {
	out := &bytes.Buffer{}
	root := NewRoot("test", Dependencies{Factory: func(string) (Manager, error) { return m, nil }, EUID: func() int { return 0 }, Stdin: strings.NewReader(""), Stdout: out, Stderr: &bytes.Buffer{}})
	return out, root
}

func TestRoutingJSONAndDegradedWarning(t *testing.T) {
	m := &fakeRoutingManager{fakeManager: &fakeManager{}, status: model.RoutingStatus{Enabled: true, State: "degraded_direct", Warp: model.WarpStatus{Configured: true}}}
	out, root := routingTestRoot(m)
	root.SetArgs([]string{"routing", "status", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"state": "degraded_direct"`) || strings.Contains(out.String(), "privateKey") {
		t.Fatalf("unexpected JSON: %s", out)
	}
}

func TestRoutingRuleAddAndNetworkConfirmation(t *testing.T) {
	m := &fakeRoutingManager{fakeManager: &fakeManager{}}
	_, root := routingTestRoot(m)
	root.SetArgs([]string{"routing", "rules", "add", "video", "--domain", "example.com", "--client", "phone", "--scope", "clients", "--priority", "7"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if m.added.ID != "video" || m.added.Scope != "clients" || len(m.added.Clients) != 1 || m.added.Priority != 7 {
		t.Fatalf("unexpected rule: %+v", m.added)
	}
	_, root = routingTestRoot(m)
	root.SetArgs([]string{"routing", "enable"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected confirmation error, got %v", err)
	}
	_, root = routingTestRoot(m)
	root.SetArgs([]string{"routing", "enable", "--yes"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !m.enableCalled {
		t.Fatal("enable was not called")
	}
}

func TestRoutingManagerUnavailable(t *testing.T) {
	_, _, root := testRoot(&fakeManager{}, "", nil)
	root.SetArgs([]string{"routing", "status"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "RoutingManager") {
		t.Fatalf("unexpected error: %v", err)
	}
}
