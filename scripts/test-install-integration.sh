#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
fixtures="$(mktemp -d)"
trap 'rm -rf -- "$test_root" "$fixtures"' EXIT

mkdir -p "$test_root/root/awg" "$test_root/etc/amnezia/amneziawg" "$fixtures/bin"
printf '%s\n' 'SCRIPT_VERSION="5.20.1"' >"$test_root/root/awg/manage_amneziawg.sh"
printf '%s\n' 'AWG_COMMON_VERSION="5.20.1"' >"$test_root/root/awg/awg_common.sh"
printf '%s\n' '[Interface]' >"$test_root/etc/amnezia/amneziawg/awg0.conf"

mock_systemctl="$fixtures/bin/systemctl"
cat >"$mock_systemctl" <<'MOCK'
#!/usr/bin/env bash
case "$1" in
  is-active|is-enabled) exit 1 ;;
  *) exit 0 ;;
esac
MOCK
chmod +x "$mock_systemctl"

good_panel="$fixtures/awgpanel-good"
cat >"$good_panel" <<'PANEL'
#!/usr/bin/env bash
if [[ "${1:-}" == "--version" ]]; then
  echo 'awgpanel version 0.3.0'
  exit 0
fi
if [[ "${1:-}" == "status" ]]; then
  echo '{"healthy":false,"serviceActive":false,"compatibility":{"ok":true}}'
  exit 1
fi
exit 0
PANEL
chmod +x "$good_panel"

run_installer() {
  AWGPANEL_INSTALL_TESTING=1 \
  AWGPANEL_INSTALL_ROOT="$test_root" \
  AWGPANEL_SYSTEMCTL="$mock_systemctl" \
    "$PROJECT_DIR/install.sh" "$@"
}

run_installer --binary="$good_panel" --non-interactive >/dev/null
installed="$test_root/usr/local/bin/awgpanel"
config="$test_root/etc/awgpanel/config.json"
test -x "$installed" || { printf '%s\n' 'бинарник не установлен' >&2; exit 1; }
[[ "$($installed --version)" == 'awgpanel version 0.3.0' ]] || { printf '%s\n' 'неверная установленная версия' >&2; exit 1; }
test -f "$config" || { printf '%s\n' 'config.json не создан' >&2; exit 1; }
! grep -q 'requiredManageMinor' "$config" || { printf '%s\n' 'новый config.json содержит устаревшую политику версии' >&2; exit 1; }
printf '%s\n' '{"custom":"preserve-me"}' >"$config"

run_installer --binary="$good_panel" --non-interactive >/dev/null
[[ "$(<"$config")" == '{"custom":"preserve-me"}' ]] || { printf '%s\n' 'config.json был перезаписан' >&2; exit 1; }

printf '%s\n' 'SCRIPT_VERSION="5.21.2"' >"$test_root/root/awg/manage_amneziawg.sh"
printf '%s\n' 'AWG_COMMON_VERSION="5.21.2"' >"$test_root/root/awg/awg_common.sh"
run_installer --binary="$good_panel" --non-interactive >/dev/null
[[ "$(<"$config")" == '{"custom":"preserve-me"}' ]] || { printf '%s\n' 'config.json был перезаписан при обновлении на AWG 5.21' >&2; exit 1; }

bad_panel="$fixtures/awgpanel-bad"
cat >"$bad_panel" <<'PANEL'
#!/usr/bin/env bash
if [[ "${1:-}" == "--version" ]]; then
  echo 'awgpanel version 0.4.0'
  exit 0
fi
if [[ "${1:-}" == "status" ]]; then
  case "$0" in
    */usr/local/bin/awgpanel)
      echo '{"healthy":false,"serviceActive":false,"compatibility":{"ok":false}}'
      ;;
    *)
      echo '{"healthy":false,"serviceActive":false,"compatibility":{"ok":true}}'
      ;;
  esac
  exit 1
fi
exit 0
PANEL
chmod +x "$bad_panel"

if run_installer --binary="$bad_panel" --non-interactive >/dev/null 2>&1; then
  printf '%s\n' 'ожидался провал post-check' >&2
  exit 1
fi
[[ "$($installed --version)" == 'awgpanel version 0.3.0' ]] || { printf '%s\n' 'rollback не восстановил бинарник' >&2; exit 1; }
[[ "$(<"$config")" == '{"custom":"preserve-me"}' ]] || { printf '%s\n' 'rollback изменил config.json' >&2; exit 1; }

printf '%s\n' 'ok - свежая установка, сохранение config и автоматический rollback'
