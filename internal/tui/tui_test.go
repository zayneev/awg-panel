package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zayneev/awg-panel/internal/manager"
	"github.com/zayneev/awg-panel/internal/model"
)

type fakeManager struct {
	clients []model.Client
	files   map[string][]byte
	created model.CreateClientRequest
}

func (f *fakeManager) Status(context.Context) model.ServerStatus {
	return model.ServerStatus{Healthy: true, ServiceActive: true, Compatibility: model.Compatibility{OK: true, ManageVersion: "5.20.1"}}
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
	value, ok := f.files[kind]
	if !ok {
		return manager.Artifact{}, os.ErrNotExist
	}
	return manager.Artifact{Data: value}, nil
}
func (f *fakeManager) Backup(context.Context) (manager.FileArtifact, error) {
	return manager.FileArtifact{}, errors.New("not implemented")
}

func press(value string) tea.KeyPressMsg {
	switch value {
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	case "backspace":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
	case "up":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyUp})
	case "down":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})
	default:
		return tea.KeyPressMsg(tea.Key{Code: []rune(value)[0], Text: value})
	}
}

func updateKey(t *testing.T, m *modelUI, value string) tea.Cmd {
	t.Helper()
	_, cmd := m.Update(press(value))
	return cmd
}

func TestDeliveryQRFlow(t *testing.T) {
	fake := &fakeManager{
		clients: []model.Client{{Name: "phone"}},
		files:   map[string][]byte{"vpn-uri": []byte("vpn://demo")},
	}
	m := &modelUI{manager: fake, screen: screenList, clients: fake.clients, width: 100, height: 30}
	updateKey(t, m, "enter")
	if m.screen != screenDetail {
		t.Fatalf("got screen %d", m.screen)
	}
	updateKey(t, m, "q")
	if m.screen != screenQRMenu {
		t.Fatalf("got screen %d", m.screen)
	}
	cmd := updateKey(t, m, "enter")
	if cmd == nil || !m.busy {
		t.Fatal("QR command was not started")
	}
	_, _ = m.Update(cmd())
	if m.screen != screenQRView || !strings.Contains(m.secretValue, "██") {
		t.Fatalf("QR was not rendered: screen=%d value=%q", m.screen, m.secretValue)
	}
	updateKey(t, m, "esc")
	if m.screen != screenDetail || m.secretValue != "" {
		t.Fatal("secret remained after closing QR")
	}
}

func TestAddClientFlow(t *testing.T) {
	fake := &fakeManager{}
	m := &modelUI{manager: fake, screen: screenList}
	updateKey(t, m, "a")
	for _, r := range "phone" {
		updateKey(t, m, string(r))
	}
	updateKey(t, m, "enter")
	if m.screen != screenAddExpiry {
		t.Fatalf("got screen %d", m.screen)
	}
	for range 4 { // permanent, 1h, 12h, 1d, 7d
		updateKey(t, m, "down")
	}
	updateKey(t, m, "enter")
	updateKey(t, m, "right")
	updateKey(t, m, "enter")
	cmd := updateKey(t, m, "enter")
	if cmd == nil {
		t.Fatal("create command was not started")
	}
	_, _ = m.Update(cmd())
	if fake.created.Name != "phone" || fake.created.Expires != "7d" || !fake.created.PSK {
		t.Fatalf("unexpected request: %+v", fake.created)
	}
}

func TestNarrowListViewKeepsEssentialColumns(t *testing.T) {
	m := &modelUI{
		version: "test", screen: screenList, width: 50, height: 24,
		status:  model.ServerStatus{ServiceActive: true, Compatibility: model.Compatibility{ManageVersion: "5.20.1"}},
		clients: []model.Client{{Name: "phone", IP: "10.8.1.2", StatusCode: "active"}},
	}
	view := m.View()
	if !strings.Contains(view.Content, "phone") || !strings.Contains(view.Content, "10.8.1.2") {
		t.Fatalf("essential data missing: %s", view.Content)
	}
	if strings.Contains(view.Content, "LAST SEEN") {
		t.Fatal("wide-only column was rendered")
	}
}

type fakeRouting struct {
	*fakeManager
	status              model.RoutingStatus
	rules               []model.RoutingRule
	added               model.RoutingRule
	warpRegistered      bool
	warpTermsAccepted   bool
	warpHealthChecked   bool
	warpRegistrationErr error
	warpImportedPath    string
	warpImportErr       error
	warpForgot          bool
	warpForgetErr       error
	warpHealthErr       error
}

func (f *fakeRouting) RoutingStatus(context.Context) model.RoutingStatus { return f.status }
func (f *fakeRouting) RoutingEnable(context.Context) error               { return nil }
func (f *fakeRouting) RoutingDisable(context.Context) error              { return nil }
func (f *fakeRouting) RoutingApply(context.Context) error                { return nil }
func (f *fakeRouting) RoutingEmergencyDisable(context.Context) error     { return nil }
func (f *fakeRouting) RoutingRules() ([]model.RoutingRule, error) {
	return append([]model.RoutingRule{}, f.rules...), nil
}
func (f *fakeRouting) RoutingRuleAdd(rule model.RoutingRule) error { f.added = rule; return nil }
func (f *fakeRouting) RoutingRuleSet(rule model.RoutingRule) error { f.added = rule; return nil }
func (f *fakeRouting) RoutingRuleToggle(string, bool) error        { return nil }
func (f *fakeRouting) RoutingRuleDelete(string) error              { return nil }
func (f *fakeRouting) RoutingWarpRegister(_ context.Context, accept bool) error {
	f.warpTermsAccepted = accept
	if f.warpRegistrationErr != nil {
		return f.warpRegistrationErr
	}
	f.warpRegistered = true
	f.status.Warp = model.WarpStatus{Configured: true, Source: "registered"}
	return nil
}
func (f *fakeRouting) RoutingWarpImport(path string) error {
	if f.warpImportErr != nil {
		return f.warpImportErr
	}
	f.warpImportedPath = path
	f.status.Warp = model.WarpStatus{Configured: true, Source: "imported"}
	return nil
}
func (f *fakeRouting) RoutingWarpTest(context.Context) (model.WarpStatus, error) {
	f.warpHealthChecked = true
	source := f.status.Warp.Source
	if source == "" {
		source = "registered"
	}
	return model.WarpStatus{Configured: true, Healthy: f.warpHealthErr == nil, Source: source}, f.warpHealthErr
}
func (f *fakeRouting) RoutingWarpForget(context.Context) error {
	if f.warpForgetErr != nil {
		return f.warpForgetErr
	}
	f.warpForgot = true
	f.status.Warp = model.WarpStatus{}
	return nil
}

func TestRoutingScreenAndDegradedWarning(t *testing.T) {
	fake := &fakeRouting{fakeManager: &fakeManager{}, status: model.RoutingStatus{Enabled: true, State: "degraded_direct"}}
	m := &modelUI{manager: fake, routing: fake, screen: screenList, width: 100, height: 30}
	cmd := updateKey(t, m, "W")
	if cmd == nil || m.screen != screenRouting {
		t.Fatal("routing screen did not open")
	}
	_, _ = m.Update(cmd())
	view := m.View().Content
	if !strings.Contains(view, "Обзор") || !strings.Contains(view, "direct-fallback") {
		t.Fatalf("unexpected routing view: %s", view)
	}
}

func TestRoutingRuleEditor(t *testing.T) {
	fake := &fakeRouting{fakeManager: &fakeManager{}}
	m := &modelUI{manager: fake, routing: fake, screen: screenRouting, routingTab: 1}
	updateKey(t, m, "a")
	for _, value := range []string{"video", "example.com"} {
		for _, r := range value {
			updateKey(t, m, string(r))
		}
		updateKey(t, m, "enter")
	}
	updateKey(t, m, "enter") // prefilled '-' means no geosite category
	// global, warp, default priority 100
	updateKey(t, m, "enter")
	updateKey(t, m, "enter")
	updateKey(t, m, "enter")
	cmd := updateKey(t, m, "enter")
	if cmd == nil {
		t.Fatalf("save command not started, screen=%d input=%q err=%v", m.screen, m.input, m.err)
	}
	_, _ = m.Update(cmd())
	if fake.added.ID != "video" || fake.added.Outbound != "warp" || len(fake.added.Domains) != 1 {
		t.Fatalf("unexpected rule: %+v", fake.added)
	}
}

func TestWarpRegistrationFromTUI(t *testing.T) {
	fake := &fakeRouting{fakeManager: &fakeManager{}, status: model.RoutingStatus{Installed: true, State: "disabled"}}
	m := &modelUI{manager: fake, routing: fake, screen: screenRouting, routingTab: 2, routingStatus: fake.status, width: 100, height: 30}
	updateKey(t, m, "g")
	if m.screen != screenRoutingWarpRegisterConfirm {
		t.Fatalf("registration confirmation did not open: screen=%d", m.screen)
	}
	view := m.View().Content
	if !strings.Contains(view, "условий Cloudflare WARP") || !strings.Contains(view, "health-check") {
		t.Fatalf("confirmation does not explain registration: %s", view)
	}
	cmd := updateKey(t, m, "enter")
	if cmd == nil || !m.busy {
		t.Fatal("registration command was not started")
	}
	_, _ = m.Update(cmd())
	if !fake.warpRegistered || !fake.warpTermsAccepted || !fake.warpHealthChecked {
		t.Fatalf("unexpected registration flow: registered=%v accepted=%v checked=%v", fake.warpRegistered, fake.warpTermsAccepted, fake.warpHealthChecked)
	}
	if m.screen != screenRouting || m.err != nil || !strings.Contains(m.notice, "health-check пройден") {
		t.Fatalf("unexpected result: screen=%d err=%v notice=%q", m.screen, m.err, m.notice)
	}
	if !m.routingStatus.Warp.Configured || !m.routingStatus.Warp.Healthy {
		t.Fatalf("WARP status was not updated: %+v", m.routingStatus.Warp)
	}
}

func TestWarpRegistrationRequiresInstalledDisabledRouting(t *testing.T) {
	for name, status := range map[string]model.RoutingStatus{
		"not installed": {State: "disabled"},
		"enabled":       {Installed: true, Enabled: true, State: "active"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeRouting{fakeManager: &fakeManager{}, status: status}
			m := &modelUI{manager: fake, routing: fake, screen: screenRouting, routingTab: 2, routingStatus: status}
			if cmd := updateKey(t, m, "g"); cmd != nil || m.screen != screenRouting {
				t.Fatalf("registration unexpectedly opened: screen=%d", m.screen)
			}
		})
	}
}

func TestWarpImportFromTUI(t *testing.T) {
	fake := &fakeRouting{fakeManager: &fakeManager{}, status: model.RoutingStatus{Installed: true, State: "disabled"}}
	m := &modelUI{manager: fake, routing: fake, screen: screenRouting, routingTab: 2, routingStatus: fake.status, width: 100, height: 30}

	updateKey(t, m, "i")
	if m.screen != screenRoutingWarpImportPath {
		t.Fatalf("import path screen did not open: screen=%d", m.screen)
	}
	for _, r := range "relative.conf" {
		updateKey(t, m, string(r))
	}
	updateKey(t, m, "enter")
	if m.screen != screenRoutingWarpImportPath || m.err == nil {
		t.Fatal("relative import path was accepted")
	}
	for range len([]rune("relative.conf")) {
		updateKey(t, m, "backspace")
	}
	for _, r := range "/root/warp.conf" {
		updateKey(t, m, string(r))
	}
	updateKey(t, m, "enter")
	if m.screen != screenRoutingWarpImportConfirm || m.warpImportPath != "/root/warp.conf" {
		t.Fatalf("unexpected import confirmation: screen=%d path=%q", m.screen, m.warpImportPath)
	}
	if view := m.View().Content; !strings.Contains(view, "credentials будут заменены") || !strings.Contains(view, "останется без изменений") {
		t.Fatalf("confirmation does not explain import: %s", view)
	}
	cmd := updateKey(t, m, "enter")
	if cmd == nil || !m.busy {
		t.Fatal("import command was not started")
	}
	_, _ = m.Update(cmd())
	if fake.warpImportedPath != "/root/warp.conf" || !fake.warpHealthChecked {
		t.Fatalf("unexpected import flow: path=%q checked=%v", fake.warpImportedPath, fake.warpHealthChecked)
	}
	if m.screen != screenRouting || m.err != nil || !strings.Contains(m.notice, "импортирован") {
		t.Fatalf("unexpected result: screen=%d err=%v notice=%q", m.screen, m.err, m.notice)
	}
	if !m.routingStatus.Warp.Configured || !m.routingStatus.Warp.Healthy || m.routingStatus.Warp.Source != "imported" {
		t.Fatalf("WARP status was not updated: %+v", m.routingStatus.Warp)
	}
}

func TestWarpForgetFromTUI(t *testing.T) {
	status := model.RoutingStatus{Installed: true, State: "disabled", Warp: model.WarpStatus{Configured: true, Source: "imported"}}
	fake := &fakeRouting{fakeManager: &fakeManager{}, status: status}
	m := &modelUI{manager: fake, routing: fake, screen: screenRouting, routingTab: 2, routingStatus: status, width: 100, height: 30}

	updateKey(t, m, "f")
	if m.screen != screenRoutingWarpForgetConfirm {
		t.Fatalf("forget confirmation did not open: screen=%d", m.screen)
	}
	if view := m.View().Content; !strings.Contains(view, "Правила доменов сохранятся") {
		t.Fatalf("confirmation does not explain forget: %s", view)
	}
	cmd := updateKey(t, m, "enter")
	if cmd == nil || !m.busy {
		t.Fatal("forget command was not started")
	}
	_, reload := m.Update(cmd())
	if !fake.warpForgot || reload == nil {
		t.Fatalf("unexpected forget flow: forgot=%v reload=%v", fake.warpForgot, reload != nil)
	}
	_, _ = m.Update(reload())
	if m.screen != screenRouting || m.err != nil || !strings.Contains(m.notice, "credentials удалены") {
		t.Fatalf("unexpected result: screen=%d err=%v notice=%q", m.screen, m.err, m.notice)
	}
	if m.routingStatus.Warp.Configured {
		t.Fatalf("WARP status was not cleared: %+v", m.routingStatus.Warp)
	}
}
