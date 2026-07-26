package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/zayneev/awg-panel/internal/config"
	"github.com/zayneev/awg-panel/internal/manager"
	"github.com/zayneev/awg-panel/internal/model"
	"github.com/zayneev/awg-panel/internal/tui"
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
	RoutingCheck(context.Context) model.RoutingCheck
	RoutingEnable(context.Context) error
	RoutingDisable(context.Context) error
	RoutingApply(context.Context) error
	RoutingEmergencyDisable(context.Context) error
	RoutingRemoveIntercept(context.Context) error
	RoutingRules() ([]model.RoutingRule, error)
	RoutingRuleAdd(model.RoutingRule) error
	RoutingRuleSet(model.RoutingRule) error
	RoutingRuleToggle(string, bool) error
	RoutingRuleDelete(string) error
	RoutingWarpRegister(context.Context, bool) error
	RoutingWarpImport(string) error
	RoutingWarpTest(context.Context) (model.WarpStatus, error)
	RoutingWarpForget(context.Context) error
	RoutingRunDNS(context.Context) error
	RoutingRecover(context.Context) error
}

type Factory func(string) (Manager, error)

type Dependencies struct {
	Factory    Factory
	EUID       func() int
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	IsTerminal func(any) bool
	RunTUI     func(Manager, string) error
}

type runtime struct {
	deps       Dependencies
	configPath string
	manager    Manager
}

type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

func Execute(version string) int {
	root := NewRoot(version, Dependencies{
		Factory: func(path string) (Manager, error) {
			cfg, err := config.Load(path)
			if err != nil {
				return nil, err
			}
			return manager.NewService(cfg), nil
		},
		EUID:   os.Geteuid,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		IsTerminal: func(value any) bool {
			file, ok := value.(*os.File)
			return ok && term.IsTerminal(int(file.Fd()))
		},
		RunTUI: func(m Manager, version string) error {
			return tui.Run(m, version)
		},
	})
	if err := root.Execute(); err != nil {
		fmt.Fprintln(root.ErrOrStderr(), "Ошибка:", err)
		var usage usageError
		if errors.As(err, &usage) {
			return 2
		}
		return 1
	}
	return 0
}

func NewRoot(version string, deps Dependencies) *cobra.Command {
	if deps.EUID == nil {
		deps.EUID = os.Geteuid
	}
	if deps.Stdin == nil {
		deps.Stdin = strings.NewReader("")
	}
	if deps.Stdout == nil {
		deps.Stdout = io.Discard
	}
	if deps.Stderr == nil {
		deps.Stderr = io.Discard
	}
	if deps.IsTerminal == nil {
		deps.IsTerminal = func(any) bool { return false }
	}
	rt := &runtime{deps: deps, configPath: config.DefaultPath}
	root := &cobra.Command{
		Use:           "awgpanel",
		Short:         "Терминальное управление AmneziaWG",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := rt.getManager()
			if err != nil {
				return err
			}
			if !deps.IsTerminal(deps.Stdin) || !deps.IsTerminal(deps.Stdout) {
				return usageError{message: "интерактивный режим требует TTY; используйте подкоманду или ssh -t"}
			}
			if deps.RunTUI == nil {
				return errors.New("TUI недоступен")
			}
			return deps.RunTUI(m, version)
		},
	}
	root.SetIn(deps.Stdin)
	root.SetOut(deps.Stdout)
	root.SetErr(deps.Stderr)
	root.PersistentFlags().StringVar(&rt.configPath, "config", config.DefaultPath, "путь к config.json")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError{message: err.Error()}
	})
	root.AddCommand(newStatusCommand(rt), newClientsCommand(rt), newRestartCommand(rt), newBackupCommand(rt), newRoutingCommand(rt), newCompletionCommand(root))
	return root
}

func (r *runtime) getManager() (Manager, error) {
	if r.deps.EUID() != 0 {
		return nil, errors.New("команда требует root; запустите через sudo")
	}
	if r.manager != nil {
		return r.manager, nil
	}
	if r.deps.Factory == nil {
		return nil, errors.New("manager не настроен")
	}
	m, err := r.deps.Factory(r.configPath)
	if err != nil {
		return nil, err
	}
	r.manager = m
	return m, nil
}

func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usageError{message: fmt.Sprintf("%s: ожидается аргументов: %d, получено: %d", cmd.CommandPath(), n, len(args))}
		}
		return nil
	}
}

func newStatusCommand(rt *runtime) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Показать состояние сервера",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := rt.getManager()
			if err != nil {
				return err
			}
			status := m.Status(cmd.Context())
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), status)
			}
			state := "DOWN"
			if status.ServiceActive {
				state = "UP"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "awg0: %s\n", state)
			fmt.Fprintf(cmd.OutOrStdout(), "Совместимость: %s", boolLabel(status.Compatibility.OK))
			if status.Compatibility.ManageVersion != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " (manage %s, common %s)", status.Compatibility.ManageVersion, status.Compatibility.CommonVersion)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			if status.Compatibility.Message != "" {
				fmt.Fprintln(cmd.OutOrStdout(), status.Compatibility.Message)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Клиенты: %d · active %d · recent %d\n", status.TotalClients, status.ActiveClients, status.RecentClients)
			fmt.Fprintf(cmd.OutOrStdout(), "Трафик: %s RX · %s TX\n", formatBytes(status.RXBytes), formatBytes(status.TXBytes))
			if status.UptimeSeconds > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Uptime: %s\n", formatDuration(time.Duration(status.UptimeSeconds)*time.Second))
			}
			if !status.Healthy {
				return errors.New("сервер не прошёл проверку состояния")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "вывести JSON")
	return cmd
}

func newClientsCommand(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{Use: "clients", Short: "Управление клиентами", Args: exactArgs(0)}
	cmd.AddCommand(
		newClientsListCommand(rt),
		newClientsShowCommand(rt),
		newClientsAddCommand(rt),
		newClientsEditCommand(rt),
		newClientsDeleteCommand(rt),
		newClientsQRCommand(rt),
		newClientsURICommand(rt),
		newClientsConfigCommand(rt),
	)
	return cmd
}

func newClientsListCommand(rt *runtime) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "list", Short: "Показать клиентов", Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := rt.getManager()
			if err != nil {
				return err
			}
			clients, err := m.Clients(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), clients)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CLIENT\tSTATUS\tIP\tRX / TX\tLAST SEEN\tEXPIRES")
			for _, client := range clients {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s / %s\t%s\t%s\n", client.Name, statusLabel(client.StatusCode), client.IP, formatBytes(client.RXBytes), formatBytes(client.TXBytes), handshakeLabel(client.LastHandshake), expiryLabel(client))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "вывести JSON")
	return cmd
}

func newClientsShowCommand(rt *runtime) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show NAME", Short: "Показать клиента", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := findClient(cmd.Context(), rt, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), client)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Клиент: %s\nIP: %s\nСтатус: %s\nRX / TX: %s / %s\nПоследний handshake: %s\nСрок: %s\n", client.Name, client.IP, statusLabel(client.StatusCode), formatBytes(client.RXBytes), formatBytes(client.TXBytes), handshakeLabel(client.LastHandshake), expiryLabel(client))
			fmt.Fprintf(cmd.OutOrStdout(), "Артефакты: conf=%s, vpn://=%s, QR=%s\n", boolLabel(client.Artifacts.Config), boolLabel(client.Artifacts.VPNURI), boolLabel(client.Artifacts.QR || client.Artifacts.VPNURIQR))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "вывести JSON")
	return cmd
}

func newClientsAddCommand(rt *runtime) *cobra.Command {
	var expires string
	var psk bool
	cmd := &cobra.Command{
		Use: "add NAME", Short: "Создать клиента", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := rt.getManager()
			if err != nil {
				return err
			}
			if err := m.CreateClient(cmd.Context(), model.CreateClientRequest{Name: args[0], Expires: expires, PSK: psk}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Клиент %s создан.\n\n", args[0])
			printDeliveryHelp(cmd.OutOrStdout(), args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&expires, "expires", "", "срок: 1h, 12h, 1d, 7d, 30d или 4w")
	cmd.Flags().BoolVar(&psk, "psk", false, "добавить PresharedKey")
	return cmd
}

func newClientsEditCommand(rt *runtime) *cobra.Command {
	var field, value string
	cmd := &cobra.Command{
		Use: "edit NAME", Short: "Изменить параметры клиента", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if field == "" || value == "" {
				return usageError{message: "обязательны --field и --value"}
			}
			m, err := rt.getManager()
			if err != nil {
				return err
			}
			if err := m.ModifyClient(cmd.Context(), args[0], model.ModifyClientRequest{Field: field, Value: value}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s для %s изменён.\n", field, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&field, "field", "", "DNS, Endpoint, AllowedIPs или PersistentKeepalive")
	cmd.Flags().StringVar(&value, "value", "", "новое значение")
	return cmd
}

func newClientsDeleteCommand(rt *runtime) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "delete NAME", Short: "Удалить клиента", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if !rt.deps.IsTerminal(rt.deps.Stdin) {
					return usageError{message: "для неинтерактивного удаления добавьте --yes"}
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Введите имя клиента %q для подтверждения: ", args[0])
				answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				if strings.TrimSpace(answer) != args[0] {
					return errors.New("удаление отменено")
				}
			}
			m, err := rt.getManager()
			if err != nil {
				return err
			}
			if err := m.DeleteClient(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Клиент %s удалён.\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "не спрашивать подтверждение")
	return cmd
}

func newClientsQRCommand(rt *runtime) *cobra.Command {
	var qrType string
	cmd := &cobra.Command{
		Use: "qr NAME", Short: "Показать QR-код", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := ""
			switch qrType {
			case "vpn":
				kind = "vpn-uri"
			case "config":
				kind = "config"
			default:
				return usageError{message: "--type должен быть vpn или config"}
			}
			m, err := rt.getManager()
			if err != nil {
				return err
			}
			artifact, err := m.Artifact(args[0], kind)
			if err != nil {
				return err
			}
			qr, err := qrcode.New(strings.TrimSpace(string(artifact.Data)), qrcode.Medium)
			if err != nil {
				return fmt.Errorf("создать QR: %w", err)
			}
			fmt.Fprint(cmd.OutOrStdout(), qr.ToSmallString(false))
			return nil
		},
	}
	cmd.Flags().StringVar(&qrType, "type", "", "тип QR: vpn или config")
	return cmd
}

func newClientsURICommand(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use: "uri NAME", Short: "Показать vpn:// URI", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := rt.getManager()
			if err != nil {
				return err
			}
			artifact, err := m.Artifact(args[0], "vpn-uri")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(string(artifact.Data)))
			return nil
		},
	}
}

func newClientsConfigCommand(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use: "config NAME", Short: "Передать существующий .conf в stdout", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if rt.deps.IsTerminal(rt.deps.Stdout) {
				return usageError{message: "не выводим приватный ключ в терминал; перенаправьте stdout в файл или используйте ssh -T"}
			}
			m, err := rt.getManager()
			if err != nil {
				return err
			}
			artifact, err := m.Artifact(args[0], "config")
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(artifact.Data)
			return err
		},
	}
}

func newRestartCommand(rt *runtime) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "restart", Short: "Перезапустить awg0", Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				if !rt.deps.IsTerminal(rt.deps.Stdin) {
					return usageError{message: "для неинтерактивного перезапуска добавьте --yes"}
				}
				fmt.Fprint(cmd.ErrOrStderr(), "Перезапустить awg0? [y/N] ")
				answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				answer = strings.ToLower(strings.TrimSpace(answer))
				if answer != "y" && answer != "yes" && answer != "д" && answer != "да" {
					return errors.New("перезапуск отменён")
				}
			}
			m, err := rt.getManager()
			if err != nil {
				return err
			}
			if err := m.Restart(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "awg0 перезапущен.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "не спрашивать подтверждение")
	return cmd
}

func newBackupCommand(rt *runtime) *cobra.Command {
	var output string
	var stdout bool
	cmd := &cobra.Command{
		Use: "backup", Short: "Создать backup", Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "" && stdout {
				return usageError{message: "используйте только один из --output или --stdout"}
			}
			if stdout && rt.deps.IsTerminal(rt.deps.Stdout) {
				return usageError{message: "бинарный backup нельзя выводить в TTY; перенаправьте stdout в файл"}
			}
			m, err := rt.getManager()
			if err != nil {
				return err
			}
			artifact, err := m.Backup(cmd.Context())
			if err != nil {
				return err
			}
			defer artifact.File.Close()
			if stdout {
				_, err = io.Copy(cmd.OutOrStdout(), artifact.File)
				return err
			}
			if output == "" {
				fmt.Fprintln(cmd.OutOrStdout(), artifact.Path)
				return nil
			}
			path, err := filepath.Abs(output)
			if err != nil {
				return err
			}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, artifact.File)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				_ = os.Remove(path)
				return errors.Join(copyErr, closeErr)
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "сохранить backup в новый файл")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "передать backup в stdout")
	return cmd
}

func newRoutingCommand(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{Use: "routing", Short: "Маршрутизация доменов через WARP", Args: exactArgs(0)}
	cmd.AddCommand(
		newRoutingStatusCommand(rt), newRoutingCheckCommand(rt),
		newRoutingActionCommand(rt, "enable", "Включить маршрутизацию", func(ctx context.Context, m RoutingManager) error { return m.RoutingEnable(ctx) }),
		newRoutingActionCommand(rt, "disable", "Выключить маршрутизацию", func(ctx context.Context, m RoutingManager) error { return m.RoutingDisable(ctx) }),
		newRoutingActionCommand(rt, "apply", "Применить правила", func(ctx context.Context, m RoutingManager) error { return m.RoutingApply(ctx) }),
		newRoutingActionCommand(rt, "emergency-disable", "Аварийно снять только перехват awgpanel", func(ctx context.Context, m RoutingManager) error { return m.RoutingEmergencyDisable(ctx) }),
		newRoutingWarpCommand(rt), newRoutingRulesCommand(rt), newRoutingInternalDNSCommand(rt), newRoutingRecoverCommand(rt), newRoutingRemoveCommand(rt),
	)
	return cmd
}

func routingManager(ctx context.Context, rt *runtime) (RoutingManager, error) {
	m, err := rt.getManager()
	if err != nil {
		return nil, err
	}
	routing, ok := m.(RoutingManager)
	if !ok {
		return nil, errors.New("RoutingManager недоступен в этой сборке")
	}
	return routing, nil
}

func newRoutingStatusCommand(rt *runtime) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "status", Short: "Показать состояние routing/WARP", Args: exactArgs(0), RunE: func(cmd *cobra.Command, _ []string) error {
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		status := m.RoutingStatus(cmd.Context())
		if asJSON {
			return writeJSON(cmd.OutOrStdout(), status)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Состояние: %s\nВключено: %s\nDNS / Xray / nftables: %s / %s / %s\nАктивных правил: %d\n", status.State, boolLabel(status.Enabled), boolLabel(status.DNSActive), boolLabel(status.XrayActive), boolLabel(status.FirewallActive), status.Rules)
		fmt.Fprintf(cmd.OutOrStdout(), "WARP: configured=%s, healthy=%s", boolLabel(status.Warp.Configured), boolLabel(status.Warp.Healthy))
		if status.Warp.EgressIP != "" {
			fmt.Fprintf(cmd.OutOrStdout(), ", egress=%s, colo=%s", status.Warp.EgressIP, status.Warp.Colo)
		}
		fmt.Fprintln(cmd.OutOrStdout())
		if status.State == "degraded_direct" {
			fmt.Fprintln(cmd.OutOrStdout(), "ВНИМАНИЕ: перехват снят; трафик временно идёт напрямую.")
		}
		if status.LastError != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "Ошибка:", status.LastError)
		}
		return nil
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "вывести JSON")
	return cmd
}

func newRoutingCheckCommand(rt *runtime) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "check", Short: "Проверить конфликты без изменения сети", Args: exactArgs(0), RunE: func(cmd *cobra.Command, _ []string) error {
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		check := m.RoutingCheck(cmd.Context())
		if asJSON {
			if err := writeJSON(cmd.OutOrStdout(), check); err != nil {
				return err
			}
		} else {
			for _, warning := range check.Warnings {
				fmt.Fprintln(cmd.OutOrStdout(), "WARN:", warning)
			}
			for _, failure := range check.Errors {
				fmt.Fprintln(cmd.OutOrStdout(), "ERROR:", failure)
			}
			if check.OK {
				fmt.Fprintln(cmd.OutOrStdout(), "Проверка пройдена.")
			}
		}
		if !check.OK {
			return errors.New("routing check не пройден")
		}
		return nil
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "вывести JSON")
	return cmd
}

func newRoutingActionCommand(rt *runtime, use, short string, action func(context.Context, RoutingManager) error) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{Use: use, Short: short, Args: exactArgs(0), RunE: func(cmd *cobra.Command, _ []string) error {
		if !yes {
			if !rt.deps.IsTerminal(rt.deps.Stdin) {
				return usageError{message: "для сетевой операции добавьте --yes"}
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "%s? [y/N] ", short)
			answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
			answer = strings.ToLower(strings.TrimSpace(answer))
			if answer != "y" && answer != "yes" && answer != "д" && answer != "да" {
				return errors.New("операция отменена")
			}
		}
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		if err := action(cmd.Context(), m); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Готово.")
		return nil
	}}
	cmd.Flags().BoolVar(&yes, "yes", false, "не спрашивать подтверждение")
	return cmd
}

func newRoutingWarpCommand(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{Use: "warp", Short: "Учётные данные и проверка WARP", Args: exactArgs(0)}
	var accept bool
	register := &cobra.Command{Use: "register", Short: "Зарегистрировать отдельное WARP-устройство", Args: exactArgs(0), RunE: func(cmd *cobra.Command, _ []string) error {
		if !accept {
			return usageError{message: "прочитайте условия Cloudflare и добавьте --accept-tos"}
		}
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		if err := m.RoutingWarpRegister(cmd.Context(), true); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Отдельное WARP-устройство зарегистрировано; секреты сохранены с правами 0600.")
		return nil
	}}
	register.Flags().BoolVar(&accept, "accept-tos", false, "подтвердить условия Cloudflare WARP")
	importCmd := &cobra.Command{Use: "import FILE", Short: "Импортировать wg-quick конфиг", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		if err := m.RoutingWarpImport(args[0]); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "WARP-конфиг импортирован; ключи не выводятся.")
		return nil
	}}
	var testJSON bool
	testCmd := &cobra.Command{Use: "test", Short: "Проверить WARP через loopback proxy", Args: exactArgs(0), RunE: func(cmd *cobra.Command, _ []string) error {
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		status, testErr := m.RoutingWarpTest(cmd.Context())
		if testJSON {
			if err := writeJSON(cmd.OutOrStdout(), status); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "WARP healthy=%s, egress=%s, colo=%s\n", boolLabel(status.Healthy), status.EgressIP, status.Colo)
		}
		return testErr
	}}
	testCmd.Flags().BoolVar(&testJSON, "json", false, "вывести JSON")
	var yes bool
	forget := &cobra.Command{Use: "forget", Short: "Удалить локальные WARP credentials", Args: exactArgs(0), RunE: func(cmd *cobra.Command, _ []string) error {
		if !yes {
			return usageError{message: "для удаления credentials добавьте --yes"}
		}
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		if err := m.RoutingWarpForget(cmd.Context()); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Локальные WARP credentials удалены.")
		return nil
	}}
	forget.Flags().BoolVar(&yes, "yes", false, "подтвердить удаление")
	cmd.AddCommand(register, importCmd, testCmd, forget)
	return cmd
}

func newRoutingRulesCommand(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{Use: "rules", Short: "Правила доменной маршрутизации", Args: exactArgs(0)}
	var asJSON bool
	list := &cobra.Command{Use: "list", Short: "Показать правила", Args: exactArgs(0), RunE: func(cmd *cobra.Command, _ []string) error {
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		rules, err := m.RoutingRules()
		if err != nil {
			return err
		}
		if asJSON {
			return writeJSON(cmd.OutOrStdout(), rules)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tON\tSCOPE\tOUTBOUND\tPRIORITY\tDOMAINS / GEOSITE")
		for _, rule := range rules {
			targets := append(append([]string{}, rule.Domains...), rule.GeoSites...)
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", rule.ID, boolLabel(rule.Enabled), rule.Scope, rule.Outbound, rule.Priority, strings.Join(targets, ","))
		}
		return w.Flush()
	}}
	list.Flags().BoolVar(&asJSON, "json", false, "вывести JSON")
	add := newRoutingRuleEditCommand(rt, false)
	set := newRoutingRuleEditCommand(rt, true)
	cmd.AddCommand(list, add, set, newRoutingRuleToggleCommand(rt, true), newRoutingRuleToggleCommand(rt, false), newRoutingRuleDeleteCommand(rt))
	return cmd
}

func newRoutingRuleEditCommand(rt *runtime, update bool) *cobra.Command {
	var domains, geosites, clients []string
	var scope, outbound string
	var priority int
	var disabled bool
	use, short := "add ID", "Добавить правило"
	if update {
		use, short = "set ID", "Изменить правило"
	}
	cmd := &cobra.Command{Use: use, Short: short, Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		rule := model.RoutingRule{ID: args[0], Enabled: !disabled, Scope: scope, Clients: clients, Domains: domains, GeoSites: geosites, Outbound: outbound, Priority: priority}
		if update {
			rules, err := m.RoutingRules()
			if err != nil {
				return err
			}
			found := false
			for _, existing := range rules {
				if existing.ID == args[0] {
					rule = existing
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("правило %s не найдено", args[0])
			}
			if cmd.Flags().Changed("scope") {
				rule.Scope = scope
			}
			if cmd.Flags().Changed("client") {
				rule.Clients = clients
			}
			if cmd.Flags().Changed("domain") {
				rule.Domains = domains
			}
			if cmd.Flags().Changed("geosite") {
				rule.GeoSites = geosites
			}
			if cmd.Flags().Changed("outbound") {
				rule.Outbound = outbound
			}
			if cmd.Flags().Changed("priority") {
				rule.Priority = priority
			}
			if cmd.Flags().Changed("disabled") {
				rule.Enabled = !disabled
			}
			if err := m.RoutingRuleSet(rule); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Правило сохранено. Если routing включён, выполните routing apply --yes.")
			return nil
		}
		if err := m.RoutingRuleAdd(rule); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Правило добавлено. Если routing включён, выполните routing apply --yes.")
		return nil
	}}
	cmd.Flags().StringSliceVar(&domains, "domain", nil, "домен; можно повторять")
	cmd.Flags().StringSliceVar(&geosites, "geosite", nil, "категория geosite; можно повторять")
	cmd.Flags().StringVar(&scope, "scope", "global", "global или clients")
	cmd.Flags().StringSliceVar(&clients, "client", nil, "AWG-клиент; можно повторять")
	cmd.Flags().StringVar(&outbound, "outbound", "warp", "warp или direct")
	cmd.Flags().IntVar(&priority, "priority", 100, "меньшее значение имеет больший приоритет")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "создать/оставить правило выключенным")
	return cmd
}

func newRoutingRuleToggleCommand(rt *runtime, enabled bool) *cobra.Command {
	use := "disable ID"
	short := "Выключить правило"
	if enabled {
		use, short = "enable ID", "Включить правило"
	}
	return &cobra.Command{Use: use, Short: short, Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		if err := m.RoutingRuleToggle(args[0], enabled); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Состояние правила изменено. Выполните routing apply --yes для применения.")
		return nil
	}}
}

func newRoutingRuleDeleteCommand(rt *runtime) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{Use: "delete ID", Short: "Удалить правило", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !yes {
			return usageError{message: "для удаления добавьте --yes"}
		}
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		if err := m.RoutingRuleDelete(args[0]); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Правило удалено. Выполните routing apply --yes для применения.")
		return nil
	}}
	cmd.Flags().BoolVar(&yes, "yes", false, "подтвердить удаление")
	return cmd
}

func newRoutingInternalDNSCommand(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{Use: "internal-dns", Hidden: true, Args: exactArgs(0), RunE: func(cmd *cobra.Command, _ []string) error {
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		return m.RoutingRunDNS(cmd.Context())
	}}
	return cmd
}

func newRoutingRecoverCommand(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{Use: "internal-recover", Hidden: true, Args: exactArgs(0), RunE: func(cmd *cobra.Command, _ []string) error {
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		return m.RoutingRecover(cmd.Context())
	}}
	return cmd
}

func newRoutingRemoveCommand(rt *runtime) *cobra.Command {
	return &cobra.Command{Use: "internal-remove-intercept", Hidden: true, Args: exactArgs(0), RunE: func(cmd *cobra.Command, _ []string) error {
		m, err := routingManager(cmd.Context(), rt)
		if err != nil {
			return err
		}
		return m.RoutingRemoveIntercept(cmd.Context())
	}}
}

func newCompletionCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use: "completion [bash|zsh|fish|powershell]", Short: "Сгенерировать shell completion", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletion(cmd.OutOrStdout())
			default:
				return usageError{message: "поддерживаются bash, zsh, fish и powershell"}
			}
		},
	}
}

func findClient(ctx context.Context, rt *runtime, name string) (model.Client, error) {
	m, err := rt.getManager()
	if err != nil {
		return model.Client{}, err
	}
	clients, err := m.Clients(ctx)
	if err != nil {
		return model.Client{}, err
	}
	for _, client := range clients {
		if client.Name == name {
			return client, nil
		}
	}
	return model.Client{}, fmt.Errorf("клиент %q не найден", name)
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printDeliveryHelp(w io.Writer, name string) {
	fmt.Fprintf(w, "QR Amnezia:  sudo awgpanel clients qr %s --type vpn\n", name)
	fmt.Fprintf(w, "QR .conf:    sudo awgpanel clients qr %s --type config\n", name)
	fmt.Fprintf(w, "vpn://:      sudo awgpanel clients uri %s\n", name)
	fmt.Fprintf(w, ".conf:       ssh -T <user>@<server> 'sudo awgpanel clients config %s' > %s.conf\n", name, name)
}

func boolLabel(value bool) string {
	if value {
		return "да"
	}
	return "нет"
}

func statusLabel(code string) string {
	switch code {
	case "active":
		return "online"
	case "recent":
		return "recent"
	case "no_handshake":
		return "never"
	case "expired":
		return "expired"
	case "disabled":
		return "disabled"
	default:
		if code == "" {
			return "unknown"
		}
		return code
	}
}

func formatBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return strconv.FormatUint(value, 10) + " B"
	}
	div, exp := uint64(unit), 0
	for n := value / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}

func handshakeLabel(value *time.Time) string {
	if value == nil {
		return "—"
	}
	delta := time.Since(*value)
	if delta < 0 {
		delta = 0
	}
	return formatDuration(delta) + " назад"
}

func expiryLabel(client model.Client) string {
	switch client.ExpiryState {
	case "permanent", "":
		return "постоянный"
	case "expired":
		return "истёк"
	case "corrupt":
		return "ошибка"
	}
	if client.ExpiresAt == nil {
		return client.ExpiryState
	}
	delta := time.Until(*client.ExpiresAt)
	if delta <= 0 {
		return "истёк"
	}
	return "через " + formatDuration(delta)
}

func formatDuration(value time.Duration) string {
	if value < time.Minute {
		seconds := int(value.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		return fmt.Sprintf("%dс", seconds)
	}
	if value < time.Hour {
		return fmt.Sprintf("%dм", int(value.Minutes()))
	}
	if value < 24*time.Hour {
		return fmt.Sprintf("%dч", int(value.Hours()))
	}
	return fmt.Sprintf("%dд", int(value.Hours()/24))
}
