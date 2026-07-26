package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/skip2/go-qrcode"

	"github.com/zayneev/awg-panel/internal/manager"
	"github.com/zayneev/awg-panel/internal/model"
)

type Manager interface {
	Status(context.Context) model.ServerStatus
	Clients(context.Context) ([]model.Client, error)
	CreateClient(context.Context, model.CreateClientRequest) error
	ModifyClient(context.Context, string, model.ModifyClientRequest) error
	DeleteClient(context.Context, string) error
	Restart(context.Context) error
	Artifact(string, string) (manager.Artifact, error)
	Backup(context.Context) (manager.FileArtifact, error)
}

type RoutingManager interface {
	RoutingStatus(context.Context) model.RoutingStatus
	RoutingEnable(context.Context) error
	RoutingDisable(context.Context) error
	RoutingApply(context.Context) error
	RoutingEmergencyDisable(context.Context) error
	RoutingRules() ([]model.RoutingRule, error)
	RoutingRuleAdd(model.RoutingRule) error
	RoutingRuleSet(model.RoutingRule) error
	RoutingRuleToggle(string, bool) error
	RoutingRuleDelete(string) error
	RoutingWarpTest(context.Context) (model.WarpStatus, error)
}

type screen int

const (
	screenList screen = iota
	screenDetail
	screenAddName
	screenAddExpiry
	screenAddPSK
	screenAddConfirm
	screenEditField
	screenEditValue
	screenEditConfirm
	screenDeleteConfirm
	screenRestartConfirm
	screenBackupConfirm
	screenQRMenu
	screenQRView
	screenURIView
	screenConfigHelp
	screenHelp
	screenRouting
	screenRoutingRuleID
	screenRoutingDomains
	screenRoutingGeoSites
	screenRoutingScope
	screenRoutingClients
	screenRoutingOutbound
	screenRoutingPriority
	screenRoutingConfirm
	screenRoutingActionConfirm
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	successStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	keyStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
)

var expiryOptions = []string{"постоянный", "1h", "12h", "1d", "7d", "30d", "4w"}
var editFields = []string{"DNS", "Endpoint", "AllowedIPs", "PersistentKeepalive"}

type modelUI struct {
	manager Manager
	routing RoutingManager
	version string

	screen  screen
	width   int
	height  int
	cursor  int
	loading bool
	busy    bool

	status  model.ServerStatus
	clients []model.Client
	err     error
	notice  string

	input       string
	choice      int
	addName     string
	addExpiry   string
	addPSK      bool
	editField   string
	editValue   string
	secretValue string
	createdName string

	routingTab    int
	routingCursor int
	routingStatus model.RoutingStatus
	routingRules  []model.RoutingRule
	routingRule   model.RoutingRule
	routingEdit   bool
	routingAction string
}

type loadMsg struct {
	status  model.ServerStatus
	clients []model.Client
	err     error
}

type operationMsg struct {
	action string
	target string
	text   string
	err    error
}

type secretMsg struct {
	kind string
	text string
	err  error
}

type tickMsg time.Time

type routingLoadMsg struct {
	status model.RoutingStatus
	rules  []model.RoutingRule
	err    error
}

func Run(m Manager, version string) error {
	routing, _ := m.(RoutingManager)
	program := tea.NewProgram(&modelUI{manager: m, routing: routing, version: version, screen: screenList, loading: true})
	_, err := program.Run()
	return err
}

func (m *modelUI) Init() tea.Cmd {
	return tea.Batch(loadCommand(m.manager), tickCommand())
}

func (m *modelUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case loadMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.status, m.clients, m.err = msg.status, msg.clients, nil
		if len(m.clients) == 0 {
			m.cursor = 0
		} else if m.createdName != "" {
			for index, client := range m.clients {
				if client.Name == m.createdName {
					m.cursor = index
					m.screen = screenDetail
					break
				}
			}
			m.createdName = ""
		} else if m.cursor >= len(m.clients) {
			m.cursor = len(m.clients) - 1
		}
		return m, nil
	case operationMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.notice = msg.text
		switch msg.action {
		case "routing":
			m.screen, m.loading = screenRouting, true
			return m, routingLoadCommand(m.routing)
		case "create":
			m.createdName = msg.target
			m.loading = true
			return m, loadCommand(m.manager)
		case "delete":
			m.screen = screenList
			m.loading = true
			return m, loadCommand(m.manager)
		case "edit", "restart":
			m.screen = screenList
			m.loading = true
			return m, loadCommand(m.manager)
		case "backup":
			m.screen = screenList
		}
		return m, nil
	case secretMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.secretValue = msg.text
		if msg.kind == "qr" {
			m.screen = screenQRView
		} else {
			m.screen = screenURIView
		}
		return m, nil
	case tickMsg:
		if !m.busy && (m.screen == screenList || m.screen == screenDetail) {
			m.loading = true
			return m, tea.Batch(loadCommand(m.manager), tickCommand())
		}
		return m, tickCommand()
	case routingLoadMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.routingStatus, m.routingRules, m.err = msg.status, msg.rules, nil
		if m.routingCursor >= len(m.routingRules) {
			m.routingCursor = max(0, len(m.routingRules)-1)
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg.String())
	}
	return m, nil
}

func (m *modelUI) handleKey(key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}
	if isInputScreen(m.screen) {
		return m.handleInputKey(key)
	}
	switch m.screen {
	case screenList:
		return m.handleListKey(key)
	case screenDetail:
		return m.handleDetailKey(key)
	case screenAddExpiry:
		return m.handleChoiceKey(key, len(expiryOptions), func() {
			m.addExpiry = expiryOptions[m.choice]
			if m.addExpiry == "постоянный" {
				m.addExpiry = ""
			}
			m.choice = 0
			m.screen = screenAddPSK
		})
	case screenAddPSK:
		switch key {
		case "left", "right", "h", "l", "y", "д":
			m.addPSK = !m.addPSK
		case "enter":
			m.screen = screenAddConfirm
		case "esc":
			m.screen = screenAddExpiry
		}
	case screenAddConfirm:
		switch key {
		case "enter", "y", "д":
			m.busy, m.err = true, nil
			request := model.CreateClientRequest{Name: m.addName, Expires: m.addExpiry, PSK: m.addPSK}
			return m, createCommand(m.manager, request)
		case "esc", "n", "н":
			m.screen = screenList
		}
	case screenEditField:
		return m.handleChoiceKey(key, len(editFields), func() {
			m.editField = editFields[m.choice]
			m.input = ""
			m.screen = screenEditValue
		})
	case screenEditConfirm:
		switch key {
		case "enter", "y", "д":
			client, ok := m.selectedClient()
			if !ok {
				m.screen = screenList
				return m, nil
			}
			m.busy, m.err = true, nil
			return m, editCommand(m.manager, client.Name, model.ModifyClientRequest{Field: m.editField, Value: m.editValue})
		case "esc", "n", "н":
			m.screen = screenDetail
		}
	case screenRestartConfirm:
		switch key {
		case "enter", "y", "д":
			m.busy, m.err = true, nil
			return m, restartCommand(m.manager)
		case "esc", "n", "н":
			m.screen = screenList
		}
	case screenBackupConfirm:
		switch key {
		case "enter", "y", "д":
			m.busy, m.err = true, nil
			return m, backupCommand(m.manager)
		case "esc", "n", "н":
			m.screen = screenList
		}
	case screenQRMenu:
		switch key {
		case "up", "k":
			if m.choice > 0 {
				m.choice--
			}
		case "down", "j":
			if m.choice < 1 {
				m.choice++
			}
		case "esc":
			m.screen = screenDetail
		case "enter":
			client, ok := m.selectedClient()
			if !ok {
				m.screen = screenList
				return m, nil
			}
			kind := "vpn-uri"
			if m.choice == 1 {
				kind = "config"
			}
			m.busy, m.err = true, nil
			return m, qrCommand(m.manager, client.Name, kind)
		}
	case screenQRView, screenURIView, screenConfigHelp, screenHelp:
		if key == "esc" || key == "enter" || key == "q" {
			m.secretValue = ""
			if m.screen == screenHelp {
				m.screen = screenList
			} else {
				m.screen = screenDetail
			}
		}
	case screenRouting:
		return m.handleRoutingKey(key)
	case screenRoutingScope:
		return m.handleChoiceKey(key, 2, func() {
			if m.choice == 0 {
				m.routingRule.Scope = "global"
				m.routingRule.Clients = nil
			} else {
				m.routingRule.Scope = "clients"
			}
			m.input = ""
			if m.routingRule.Scope == "clients" {
				m.input = strings.Join(m.routingRule.Clients, ",")
				m.screen = screenRoutingClients
			} else {
				if m.routingRule.Outbound == "direct" {
					m.choice = 1
				} else {
					m.choice = 0
				}
				m.screen = screenRoutingOutbound
			}
		})
	case screenRoutingOutbound:
		return m.handleChoiceKey(key, 2, func() {
			if m.choice == 0 {
				m.routingRule.Outbound = "warp"
			} else {
				m.routingRule.Outbound = "direct"
			}
			m.input = strconv.Itoa(m.routingRule.Priority)
			m.screen = screenRoutingPriority
		})
	case screenRoutingConfirm:
		switch key {
		case "enter", "y", "д":
			m.busy, m.err = true, nil
			return m, routingRuleSaveCommand(m.routing, m.routingRule, m.routingEdit)
		case "esc", "n", "н":
			m.screen = screenRouting
		}
	case screenRoutingActionConfirm:
		switch key {
		case "enter", "y", "д":
			m.busy, m.err = true, nil
			return m, routingActionCommand(m.routing, m.routingAction)
		case "esc", "n", "н":
			m.screen = screenRouting
		}
	}
	return m, nil
}

func (m *modelUI) handleListKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor+1 < len(m.clients) {
			m.cursor++
		}
	case "enter":
		if len(m.clients) > 0 {
			m.screen = screenDetail
		}
	case "a":
		m.resetAdd()
		m.screen = screenAddName
	case "r":
		m.loading, m.err = true, nil
		return m, loadCommand(m.manager)
	case "s":
		m.screen = screenRestartConfirm
	case "b":
		m.screen = screenBackupConfirm
	case "?":
		m.screen = screenHelp
	case "w", "W":
		if m.routing == nil {
			m.err = errors.New("RoutingManager недоступен в этой сборке")
			return m, nil
		}
		m.screen, m.routingTab, m.loading, m.err = screenRouting, 0, true, nil
		return m, routingLoadCommand(m.routing)
	case "q", "esc":
		return m, tea.Quit
	}
	return m, nil
}

func (m *modelUI) handleDetailKey(key string) (tea.Model, tea.Cmd) {
	client, ok := m.selectedClient()
	if !ok {
		m.screen = screenList
		return m, nil
	}
	switch key {
	case "q":
		m.choice = 0
		m.screen = screenQRMenu
	case "u":
		m.busy, m.err = true, nil
		return m, uriCommand(m.manager, client.Name)
	case "c":
		m.screen = screenConfigHelp
	case "e":
		m.choice = 0
		m.screen = screenEditField
	case "d":
		m.input = ""
		m.screen = screenDeleteConfirm
	case "esc", "backspace":
		m.screen = screenList
	case "w", "W":
		if m.routing == nil {
			m.err = errors.New("RoutingManager недоступен в этой сборке")
			return m, nil
		}
		m.screen, m.routingTab, m.loading = screenRouting, 0, true
		return m, routingLoadCommand(m.routing)
	}
	return m, nil
}

func (m *modelUI) handleRoutingKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.screen = screenList
		return m, nil
	case "1":
		m.routingTab = 0
	case "2":
		m.routingTab = 1
	case "3":
		m.routingTab = 2
	case "left", "h":
		if m.routingTab > 0 {
			m.routingTab--
		}
	case "right", "l":
		if m.routingTab < 2 {
			m.routingTab++
		}
	case "r":
		m.loading = true
		return m, routingLoadCommand(m.routing)
	}
	switch m.routingTab {
	case 0:
		switch key {
		case "e":
			if m.routingStatus.Enabled {
				m.routingAction = "disable"
			} else {
				m.routingAction = "enable"
			}
			m.screen = screenRoutingActionConfirm
		case "a":
			m.routingAction, m.screen = "apply", screenRoutingActionConfirm
		case "x":
			m.routingAction, m.screen = "emergency", screenRoutingActionConfirm
		}
	case 1:
		switch key {
		case "up", "k":
			if m.routingCursor > 0 {
				m.routingCursor--
			}
		case "down", "j":
			if m.routingCursor+1 < len(m.routingRules) {
				m.routingCursor++
			}
		case "a":
			m.routingRule = model.RoutingRule{Enabled: true, Scope: "global", Outbound: "warp", Priority: 100}
			m.routingEdit = false
			m.input = ""
			m.screen = screenRoutingRuleID
		case "enter", "e":
			if len(m.routingRules) > 0 {
				m.routingRule = m.routingRules[m.routingCursor]
				m.routingEdit = true
				m.input = strings.Join(m.routingRule.Domains, ",")
				if m.input == "" {
					m.input = "-"
				}
				m.screen = screenRoutingDomains
			}
		case " ":
			if len(m.routingRules) > 0 {
				rule := m.routingRules[m.routingCursor]
				m.busy = true
				return m, routingToggleCommand(m.routing, rule.ID, !rule.Enabled)
			}
		case "d":
			if len(m.routingRules) > 0 {
				m.routingAction = "rule-delete:" + m.routingRules[m.routingCursor].ID
				m.screen = screenRoutingActionConfirm
			}
		}
	case 2:
		if key == "t" {
			m.busy = true
			return m, routingActionCommand(m.routing, "test")
		}
	}
	return m, nil
}

func (m *modelUI) handleInputKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		switch m.screen {
		case screenAddName:
			m.screen = screenList
		case screenEditValue, screenDeleteConfirm:
			m.screen = screenDetail
		case screenRoutingRuleID, screenRoutingDomains, screenRoutingGeoSites, screenRoutingClients, screenRoutingPriority:
			m.screen = screenRouting
		}
		m.input = ""
		return m, nil
	case "backspace":
		m.input = trimLastRune(m.input)
		return m, nil
	case "enter":
		value := strings.TrimSpace(m.input)
		if value == "" {
			m.err = errors.New("значение не может быть пустым")
			return m, nil
		}
		switch m.screen {
		case screenAddName:
			m.addName, m.choice, m.err = value, 0, nil
			m.input = ""
			m.screen = screenAddExpiry
		case screenEditValue:
			m.editValue, m.err = value, nil
			m.input = ""
			m.screen = screenEditConfirm
		case screenDeleteConfirm:
			client, ok := m.selectedClient()
			if !ok {
				m.screen = screenList
				return m, nil
			}
			if value != client.Name {
				m.err = errors.New("имя не совпадает")
				return m, nil
			}
			m.busy, m.err = true, nil
			return m, deleteCommand(m.manager, client.Name)
		case screenRoutingRuleID:
			m.routingRule.ID, m.input, m.err, m.screen = value, "", nil, screenRoutingDomains
		case screenRoutingDomains:
			if value == "-" {
				m.routingRule.Domains = nil
			} else {
				m.routingRule.Domains = commaValues(value)
			}
			m.input, m.screen = strings.Join(m.routingRule.GeoSites, ","), screenRoutingGeoSites
			if m.input == "" {
				m.input = "-"
			}
		case screenRoutingGeoSites:
			if value == "-" {
				m.routingRule.GeoSites = nil
			} else {
				m.routingRule.GeoSites = commaValues(value)
			}
			m.input, m.choice, m.screen = "", 0, screenRoutingScope
			if m.routingRule.Scope == "clients" {
				m.choice = 1
			}
		case screenRoutingClients:
			m.routingRule.Clients, m.input, m.choice, m.screen = commaValues(value), "", 0, screenRoutingOutbound
			if m.routingRule.Outbound == "direct" {
				m.choice = 1
			}
		case screenRoutingPriority:
			priority, err := strconv.Atoi(value)
			if err != nil {
				m.err = errors.New("priority должен быть числом")
				return m, nil
			}
			m.routingRule.Priority, m.input, m.screen = priority, "", screenRoutingConfirm
		}
		return m, nil
	}
	if isPrintableInput(key) && len(m.input) < 4096 {
		m.input += key
		m.err = nil
	}
	return m, nil
}

func (m *modelUI) handleChoiceKey(key string, count int, accept func()) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.choice > 0 {
			m.choice--
		}
	case "down", "j":
		if m.choice+1 < count {
			m.choice++
		}
	case "enter":
		accept()
	case "esc":
		switch m.screen {
		case screenAddExpiry:
			m.screen = screenAddName
		case screenEditField, screenQRMenu:
			m.screen = screenDetail
		case screenRoutingScope, screenRoutingOutbound:
			m.screen = screenRouting
		}
	}
	return m, nil
}

func (m *modelUI) View() tea.View {
	var content string
	if m.loading && len(m.clients) == 0 {
		content = titleStyle.Render("AWG Panel") + "\n\nЗагрузка…"
	} else {
		switch m.screen {
		case screenList:
			content = m.viewList()
		case screenDetail:
			content = m.viewDetail()
		case screenAddName:
			content = m.viewInput("Новый клиент", "Имя клиента", "латинские буквы, цифры, _ и -")
		case screenAddExpiry:
			content = m.viewChoice("Срок действия", expiryOptions)
		case screenAddPSK:
			value := "нет"
			if m.addPSK {
				value = "да"
			}
			content = titleStyle.Render("PresharedKey") + "\n\nДобавить PSK: " + selectedStyle.Render(value) + "\n\n←/→ изменить · Enter продолжить · Esc назад"
		case screenAddConfirm:
			expiry := m.addExpiry
			if expiry == "" {
				expiry = "постоянный"
			}
			content = titleStyle.Render("Создать клиента?") + fmt.Sprintf("\n\nИмя: %s\nСрок: %s\nPSK: %s\n\nEnter подтвердить · Esc отменить", m.addName, expiry, yesNo(m.addPSK))
		case screenEditField:
			content = m.viewChoice("Что изменить", editFields)
		case screenEditValue:
			content = m.viewInput("Изменить "+m.editField, "Новое значение", "Enter продолжить · Esc отменить")
		case screenEditConfirm:
			content = titleStyle.Render("Сохранить изменение?") + fmt.Sprintf("\n\n%s = %s\n\nEnter подтвердить · Esc отменить", m.editField, m.editValue)
		case screenDeleteConfirm:
			client, _ := m.selectedClient()
			content = m.viewInput("Удалить "+client.Name, "Введите точное имя клиента", "конфиг, ключи и peer будут удалены")
		case screenRestartConfirm:
			content = titleStyle.Render("Перезапустить awg0?") + "\n\nАктивные туннели кратковременно отключатся.\n\nEnter подтвердить · Esc отменить"
		case screenBackupConfirm:
			content = titleStyle.Render("Создать backup?") + "\n\nАрхив будет сохранён в /root/awg/backups.\n\nEnter подтвердить · Esc отменить"
		case screenQRMenu:
			content = m.viewChoice("Выберите QR", []string{"Amnezia Client — vpn://", "AmneziaWG — .conf"})
		case screenQRView:
			content = titleStyle.Render("QR-код") + "\n\n" + m.secretValue + "\nEsc закрыть"
		case screenURIView:
			content = titleStyle.Render("vpn://") + "\n\n" + wrapText(m.secretValue, max(30, m.width-4)) + "\n\nEsc закрыть"
		case screenConfigHelp:
			client, _ := m.selectedClient()
			command := fmt.Sprintf("umask 077\nssh -T <user>@<server> 'sudo awgpanel clients config %s' > %s.conf", client.Name, client.Name)
			content = titleStyle.Render("Сохранить .conf локально") + "\n\nЗапустите на своём компьютере:\n\n" + command + "\n\nИсходный файл на VPS не изменяется.\nEsc назад"
		case screenHelp:
			content = titleStyle.Render("Горячие клавиши") + "\n\n↑/↓, j/k  выбрать клиента\nEnter      открыть карточку\nA          добавить клиента\nW          routing / WARP\nR          обновить\nB          создать backup\nS          перезапустить awg0\nEsc        назад или выход\nCtrl+C     немедленный выход"
		case screenRouting:
			content = m.viewRouting()
		case screenRoutingRuleID:
			content = m.viewInput("Новое routing-правило", "ID", "a-z, 0-9, _ и -")
		case screenRoutingDomains:
			content = m.viewInput("Routing-правило", "Домены через запятую", "обычный домен включает поддомены; '-' — без доменов")
		case screenRoutingGeoSites:
			content = m.viewInput("Routing-правило", "Geosite через запятую", "например google,openai; '-' — без категорий")
		case screenRoutingScope:
			content = m.viewChoice("Область правила", []string{"Все клиенты", "Выбранные клиенты"})
		case screenRoutingClients:
			content = m.viewInput("Routing-правило", "Клиенты через запятую", "неизвестный клиент не станет global-правилом")
		case screenRoutingOutbound:
			content = m.viewChoice("Маршрут", []string{"WARP", "Direct"})
		case screenRoutingPriority:
			content = m.viewInput("Routing-правило", "Priority", "меньшее число применяется раньше")
		case screenRoutingConfirm:
			content = titleStyle.Render("Сохранить routing-правило?") + fmt.Sprintf("\n\nID: %s\nScope: %s\nClients: %s\nDomains: %s\nGeosite: %s\nOutbound: %s\nPriority: %d\n\nEnter подтвердить · Esc отменить", m.routingRule.ID, m.routingRule.Scope, strings.Join(m.routingRule.Clients, ","), strings.Join(m.routingRule.Domains, ","), strings.Join(m.routingRule.GeoSites, ","), m.routingRule.Outbound, m.routingRule.Priority)
		case screenRoutingActionConfirm:
			content = titleStyle.Render("Подтвердите сетевую операцию") + "\n\n" + m.routingAction + "\n\nИзменяются только объекты awgpanel.\nEnter подтвердить · Esc отменить"
		}
	}
	if m.busy {
		content += "\n\n" + mutedStyle.Render("Выполняется операция…")
	}
	if m.err != nil {
		content += "\n\n" + errorStyle.Render("Ошибка: "+m.err.Error())
	} else if m.notice != "" && (m.screen == screenList || m.screen == screenRouting) {
		content += "\n\n" + successStyle.Render(m.notice)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "awgpanel"
	return view
}

func (m *modelUI) viewList() string {
	state := "DOWN"
	if m.status.ServiceActive {
		state = "UP"
	}
	header := fmt.Sprintf("%s  awg0 %s  clients %d  manage %s", titleStyle.Render("AWG Panel "+m.version), state, len(m.clients), emptyDash(m.status.Compatibility.ManageVersion))
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", max(20, min(m.width, 96))))
	b.WriteString("\n")
	wide := m.width >= 92
	medium := m.width >= 68
	if wide {
		b.WriteString(fmt.Sprintf("  %-20s %-9s %-15s %12s %12s\n", "CLIENT", "STATUS", "IP", "RX / TX", "LAST SEEN"))
	} else if medium {
		b.WriteString(fmt.Sprintf("  %-20s %-9s %-15s %12s\n", "CLIENT", "STATUS", "IP", "RX / TX"))
	} else {
		b.WriteString(fmt.Sprintf("  %-20s %-9s %-15s\n", "CLIENT", "STATUS", "IP"))
	}
	if len(m.clients) == 0 {
		b.WriteString("\nКлиентов пока нет. Нажмите A, чтобы создать первого.\n")
	}
	maxRows := max(3, m.height-8)
	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := min(len(m.clients), start+maxRows)
	for index := start; index < end; index++ {
		client := m.clients[index]
		prefix := "  "
		if index == m.cursor {
			prefix = "> "
		}
		name := truncate(client.Name, 20)
		var row string
		if wide {
			row = fmt.Sprintf("%s%-20s %-9s %-15s %5s/%-6s %12s", prefix, name, tuiStatus(client.StatusCode), client.IP, shortBytes(client.RXBytes), shortBytes(client.TXBytes), shortHandshake(client.LastHandshake))
		} else if medium {
			row = fmt.Sprintf("%s%-20s %-9s %-15s %5s/%-6s", prefix, name, tuiStatus(client.StatusCode), client.IP, shortBytes(client.RXBytes), shortBytes(client.TXBytes))
		} else {
			row = fmt.Sprintf("%s%-20s %-9s %-15s", prefix, name, tuiStatus(client.StatusCode), client.IP)
		}
		if index == m.cursor {
			row = selectedStyle.Render(row)
		}
		b.WriteString(row + "\n")
	}
	b.WriteString("\n")
	b.WriteString(keyStyle.Render("Enter") + " детали  " + keyStyle.Render("A") + " добавить  " + keyStyle.Render("W") + " routing  " + keyStyle.Render("R") + " обновить  " + keyStyle.Render("B") + " backup  " + keyStyle.Render("S") + " restart  " + keyStyle.Render("Esc") + " выход")
	return b.String()
}

func (m *modelUI) viewRouting() string {
	tabs := []string{"[1] Обзор", "[2] Правила", "[3] WARP"}
	tabs[m.routingTab] = selectedStyle.Render(tabs[m.routingTab])
	header := titleStyle.Render("Routing / WARP") + "\n" + strings.Join(tabs, "  ") + "\n\n"
	switch m.routingTab {
	case 0:
		warning := ""
		if m.routingStatus.State == "degraded_direct" {
			warning = "\n" + errorStyle.Render("ВНИМАНИЕ: временный direct-fallback")
		}
		return header + fmt.Sprintf("Состояние: %s\nВключено: %s\nDNS: %s\nXray: %s\nnftables: %s\nТребуется apply: %s\nWARP healthy: %s%s\n\n[E] enable/disable  [A] apply  [X] emergency-disable  [R] refresh  [Esc] назад", m.routingStatus.State, yesNo(m.routingStatus.Enabled), yesNo(m.routingStatus.DNSActive), yesNo(m.routingStatus.XrayActive), yesNo(m.routingStatus.FirewallActive), yesNo(m.routingStatus.NeedsApply), yesNo(m.routingStatus.Warp.Healthy), warning)
	case 1:
		var b strings.Builder
		b.WriteString(header)
		if len(m.routingRules) == 0 {
			b.WriteString("Правил пока нет.\n")
		}
		for i, rule := range m.routingRules {
			line := fmt.Sprintf("  %-18s %-7s %-7s p=%d  %s", rule.ID, rule.Outbound, rule.Scope, rule.Priority, strings.Join(append(append([]string{}, rule.Domains...), rule.GeoSites...), ","))
			if !rule.Enabled {
				line += " (off)"
			}
			if i == m.routingCursor {
				line = selectedStyle.Render("> " + strings.TrimPrefix(line, "  "))
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n[A] добавить  [Enter/E] изменить  [Space] on/off  [D] удалить  [Esc] назад")
		return b.String()
	default:
		warp := m.routingStatus.Warp
		return header + fmt.Sprintf("Настроен: %s\nИсточник: %s\nEndpoint: %s\nHealthy: %s\nEgress IP: %s\nColo: %s\n\n[T] проверить WARP  [Esc] назад\n\nРегистрация и импорт секретного конфига доступны через CLI.", yesNo(warp.Configured), emptyDash(warp.Source), emptyDash(warp.Endpoint), yesNo(warp.Healthy), emptyDash(warp.EgressIP), emptyDash(warp.Colo))
	}
}

func (m *modelUI) viewDetail() string {
	client, ok := m.selectedClient()
	if !ok {
		return "Клиент не найден"
	}
	return fmt.Sprintf("%s\n\nIP: %s\nIPv6: %s\nСтатус: %s\nТрафик RX / TX: %s / %s\nПоследний handshake: %s\nСрок: %s\n\n%s QR   %s vpn://   %s сохранить .conf\n%s изменить   %s удалить   %s назад",
		titleStyle.Render("Клиент "+client.Name), emptyDash(client.IP), emptyDash(client.ClientIPv6), tuiStatus(client.StatusCode), fullBytes(client.RXBytes), fullBytes(client.TXBytes), fullHandshake(client.LastHandshake), tuiExpiry(client), keyStyle.Render("[Q]"), keyStyle.Render("[U]"), keyStyle.Render("[C]"), keyStyle.Render("[E]"), keyStyle.Render("[D]"), keyStyle.Render("[Esc]"))
}

func (m *modelUI) viewChoice(title string, choices []string) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(title) + "\n\n")
	for index, choice := range choices {
		line := "  " + choice
		if index == m.choice {
			line = selectedStyle.Render("> " + choice)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n↑/↓ выбрать · Enter продолжить · Esc назад")
	return b.String()
}

func (m *modelUI) viewInput(title, label, hint string) string {
	return titleStyle.Render(title) + "\n\n" + label + ": " + m.input + "█\n\n" + mutedStyle.Render(hint) + "\nEnter продолжить · Esc назад"
}

func (m *modelUI) selectedClient() (model.Client, bool) {
	if m.cursor < 0 || m.cursor >= len(m.clients) {
		return model.Client{}, false
	}
	return m.clients[m.cursor], true
}

func (m *modelUI) resetAdd() {
	m.input, m.addName, m.addExpiry = "", "", ""
	m.addPSK, m.choice, m.err, m.notice = false, 0, nil, ""
}

func loadCommand(m Manager) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
		defer cancel()
		status := m.Status(ctx)
		clients, err := m.Clients(ctx)
		return loadMsg{status: status, clients: clients, err: err}
	}
}

func tickCommand() tea.Cmd {
	return tea.Tick(5*time.Second, func(value time.Time) tea.Msg { return tickMsg(value) })
}

func createCommand(m Manager, request model.CreateClientRequest) tea.Cmd {
	return func() tea.Msg {
		err := m.CreateClient(context.Background(), request)
		return operationMsg{action: "create", target: request.Name, text: "Клиент " + request.Name + " создан.", err: err}
	}
}

func editCommand(m Manager, name string, request model.ModifyClientRequest) tea.Cmd {
	return func() tea.Msg {
		err := m.ModifyClient(context.Background(), name, request)
		return operationMsg{action: "edit", target: name, text: request.Field + " изменён.", err: err}
	}
}

func deleteCommand(m Manager, name string) tea.Cmd {
	return func() tea.Msg {
		err := m.DeleteClient(context.Background(), name)
		return operationMsg{action: "delete", target: name, text: "Клиент " + name + " удалён.", err: err}
	}
}

func restartCommand(m Manager) tea.Cmd {
	return func() tea.Msg {
		err := m.Restart(context.Background())
		return operationMsg{action: "restart", text: "awg0 перезапущен.", err: err}
	}
}

func backupCommand(m Manager) tea.Cmd {
	return func() tea.Msg {
		artifact, err := m.Backup(context.Background())
		if err != nil {
			return operationMsg{action: "backup", err: err}
		}
		_ = artifact.File.Close()
		return operationMsg{action: "backup", text: "Backup создан: " + artifact.Path}
	}
}

func routingLoadCommand(m RoutingManager) tea.Cmd {
	return func() tea.Msg {
		if m == nil {
			return routingLoadMsg{err: errors.New("RoutingManager недоступен")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		status := m.RoutingStatus(ctx)
		rules, err := m.RoutingRules()
		return routingLoadMsg{status: status, rules: rules, err: err}
	}
}

func routingActionCommand(m RoutingManager, action string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		var err error
		text := "Routing обновлён."
		switch {
		case action == "enable":
			err = m.RoutingEnable(ctx)
			text = "Routing включён."
		case action == "disable":
			err = m.RoutingDisable(ctx)
			text = "Routing выключен."
		case action == "apply":
			err = m.RoutingApply(ctx)
			text = "Routing-правила применены."
		case action == "emergency":
			err = m.RoutingEmergencyDisable(ctx)
			text = "Перехват awgpanel аварийно снят."
		case action == "test":
			_, err = m.RoutingWarpTest(ctx)
			text = "WARP health-check пройден."
		case strings.HasPrefix(action, "rule-delete:"):
			err = m.RoutingRuleDelete(strings.TrimPrefix(action, "rule-delete:"))
			text = "Правило удалено."
		default:
			err = errors.New("неизвестная routing-операция")
		}
		return operationMsg{action: "routing", text: text, err: err}
	}
}

func routingRuleSaveCommand(m RoutingManager, rule model.RoutingRule, update bool) tea.Cmd {
	return func() tea.Msg {
		var err error
		if update {
			err = m.RoutingRuleSet(rule)
		} else {
			err = m.RoutingRuleAdd(rule)
		}
		return operationMsg{action: "routing", text: "Routing-правило сохранено.", err: err}
	}
}

func routingToggleCommand(m RoutingManager, id string, enabled bool) tea.Cmd {
	return func() tea.Msg {
		return operationMsg{action: "routing", text: "Состояние правила изменено.", err: m.RoutingRuleToggle(id, enabled)}
	}
}

func commaValues(value string) []string {
	parts := strings.Split(value, ",")
	result := parts[:0]
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func qrCommand(m Manager, name, kind string) tea.Cmd {
	return func() tea.Msg {
		artifact, err := m.Artifact(name, kind)
		if err != nil {
			return secretMsg{kind: "qr", err: err}
		}
		qr, err := qrcode.New(strings.TrimSpace(string(artifact.Data)), qrcode.Medium)
		if err != nil {
			return secretMsg{kind: "qr", err: err}
		}
		return secretMsg{kind: "qr", text: qr.ToSmallString(false)}
	}
}

func uriCommand(m Manager, name string) tea.Cmd {
	return func() tea.Msg {
		artifact, err := m.Artifact(name, "vpn-uri")
		if err != nil {
			return secretMsg{kind: "uri", err: err}
		}
		return secretMsg{kind: "uri", text: strings.TrimSpace(string(artifact.Data))}
	}
}

func isInputScreen(value screen) bool {
	return value == screenAddName || value == screenEditValue || value == screenDeleteConfirm || value == screenRoutingRuleID || value == screenRoutingDomains || value == screenRoutingGeoSites || value == screenRoutingClients || value == screenRoutingPriority
}

func isPrintableInput(value string) bool {
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func trimLastRune(value string) string {
	_, size := utf8.DecodeLastRuneInString(value)
	if size <= 0 {
		return ""
	}
	return value[:len(value)-size]
}

func yesNo(value bool) string {
	if value {
		return "да"
	}
	return "нет"
}

func tuiStatus(value string) string {
	switch value {
	case "active":
		return "● online"
	case "recent":
		return "◐ recent"
	case "no_handshake":
		return "○ never"
	case "expired":
		return "× expired"
	case "disabled":
		return "× disabled"
	default:
		if value == "" {
			return "? unknown"
		}
		return value
	}
}

func shortBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%dB", value)
	}
	units := []string{"K", "M", "G", "T", "P"}
	number := float64(value)
	index := -1
	for number >= unit && index+1 < len(units) {
		number /= unit
		index++
	}
	return fmt.Sprintf("%.1f%s", number, units[index])
}

func fullBytes(value uint64) string {
	short := shortBytes(value)
	if strings.HasSuffix(short, "B") {
		return short
	}
	return short + "iB"
}

func shortHandshake(value *time.Time) string {
	if value == nil {
		return "—"
	}
	delta := time.Since(*value)
	if delta < time.Minute {
		return fmt.Sprintf("%ds", max(0, int(delta.Seconds())))
	}
	if delta < time.Hour {
		return fmt.Sprintf("%dm", int(delta.Minutes()))
	}
	if delta < 24*time.Hour {
		return fmt.Sprintf("%dh", int(delta.Hours()))
	}
	return fmt.Sprintf("%dd", int(delta.Hours()/24))
}

func fullHandshake(value *time.Time) string {
	if value == nil {
		return "—"
	}
	return value.Local().Format("2006-01-02 15:04:05") + " (" + shortHandshake(value) + " назад)"
}

func tuiExpiry(client model.Client) string {
	if client.ExpiryState == "permanent" || client.ExpiryState == "" {
		return "постоянный"
	}
	if client.ExpiryState == "expired" {
		return "истёк"
	}
	if client.ExpiryState == "corrupt" {
		return "повреждён"
	}
	if client.ExpiresAt == nil {
		return client.ExpiryState
	}
	return client.ExpiresAt.Local().Format("2006-01-02 15:04")
}

func emptyDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func truncate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

func wrapText(value string, width int) string {
	if width < 1 {
		return value
	}
	var b strings.Builder
	for len(value) > width {
		b.WriteString(value[:width])
		b.WriteByte('\n')
		value = value[width:]
	}
	b.WriteString(value)
	return b.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
