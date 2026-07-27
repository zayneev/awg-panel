#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AWGPANEL_INSTALL_LIB_ONLY=1
# shellcheck source=../install.sh
source "$PROJECT_DIR/install.sh"

tests=0
failures=0

pass() {
  tests=$((tests + 1))
  printf 'ok %d - %s\n' "$tests" "$1"
}

fail() {
  tests=$((tests + 1))
  failures=$((failures + 1))
  printf 'not ok %d - %s\n' "$tests" "$1"
}

assert_equal() {
  local name="$1" expected="$2" actual="$3"
  if [[ "$expected" == "$actual" ]]; then pass "$name"; else fail "$name (ожидалось: $expected; получено: $actual)"; fi
}

assert_success() {
  local name="$1"
  shift
  if "$@"; then pass "$name"; else fail "$name"; fi
}

assert_failure() {
  local name="$1"
  shift
  if "$@"; then fail "$name"; else pass "$name"; fi
}

assert_equal 'amd64 из x86_64' amd64 "$(machine_arch x86_64)"
assert_equal 'arm64 из aarch64' arm64 "$(machine_arch aarch64)"
assert_failure 'неподдерживаемая архитектура' machine_arch riscv64
assert_equal 'имя amd64 asset' awgpanel-linux-amd64 "$(panel_asset x86_64)"
assert_equal 'URL latest' "https://github.com/zayneev/awg-panel/releases/latest/download" "$(release_base_url '')"
assert_equal 'URL версии' "https://github.com/zayneev/awg-panel/releases/download/v0.3.0" "$(release_base_url 0.3.0)"
assert_equal 'minor из patch-версии' 5.21 "$(version_minor 5.21.2)"
assert_failure 'неполная версия не имеет minor' version_minor 5.21
assert_success 'поддерживается AWG 5.20' is_supported_awg_minor 5.20
assert_success 'поддерживается AWG 5.21' is_supported_awg_minor 5.21
assert_failure 'AWG 5.22 пока не поддерживается' is_supported_awg_minor 5.22

assert_success 'здоровый status JSON' classify_status_json '{"serviceActive":true,"compatibility":{"ok":true}}'
assert_success 'здоровый форматированный status JSON' classify_status_json '{
  "healthy": true,
  "serviceActive": true,
  "compatibility": {
    "ok": true,
    "supportedMinors": ["5.20", "5.21"]
  }
}'
set +e
classify_status_json '{"serviceActive":false,"compatibility":{"ok":true}}'
rc=$?
set -e
assert_equal 'неактивный awg0 — предупреждение' 1 "$rc"
set +e
classify_status_json '{"serviceActive":true,"compatibility":{"ok":false}}'
rc=$?
set -e
assert_equal 'несовместимый AWG — ошибка' 2 "$rc"

(
  PANEL_BINARY=''
  REQUESTED_VERSION=''
  WITH_ROUTING=0
  ROUTING_EXPLICIT=0
  XRAY_ARCHIVE=''
  PURGE_LEGACY=0
  INTERACTIVE_REQUESTED=0
  NON_INTERACTIVE_REQUESTED=0
  parse_args --version=v0.4.1 --with-routing --non-interactive
  [[ "$REQUESTED_VERSION" == 0.4.1 && "$WITH_ROUTING" -eq 1 && "$NON_INTERACTIVE_REQUESTED" -eq 1 ]]
)
if [[ $? -eq 0 ]]; then pass 'разбор параметров'; else fail 'разбор параметров'; fi

assert_failure 'конфликт binary и version' bash -c "AWGPANEL_INSTALL_LIB_ONLY=1 source '$PROJECT_DIR/install.sh'; parse_args --binary=/tmp/panel --version=1.0.0 >/dev/null 2>&1"
assert_failure 'xray archive требует routing' bash -c "AWGPANEL_INSTALL_LIB_ONLY=1 source '$PROJECT_DIR/install.sh'; parse_args --xray-archive=/tmp/x.zip >/dev/null 2>&1"

tmp="$(mktemp -d)"
trap 'rm -rf -- "$tmp"' EXIT

check_versions() {
  local manage="$1" common="$2"
  printf 'SCRIPT_VERSION="%s"\n' "$manage" >"$tmp/manage.sh"
  printf 'AWG_COMMON_VERSION="%s"\n' "$common" >"$tmp/common.sh"
  (MANAGE_SCRIPT="$tmp/manage.sh" COMMON_SCRIPT="$tmp/common.sh" check_awg_compatibility >/dev/null)
}

assert_success 'совместимы разные patch 5.20' check_versions 5.20.1 5.20.9
assert_success 'совместимы разные patch 5.21' check_versions 5.21.2 5.21.0
assert_failure 'смешанные minor несовместимы' check_versions 5.20.1 5.21.2
assert_failure 'будущий minor отклоняется' check_versions 5.22.0 5.22.1

printf 'old\n' >"$tmp/target"
BACKUP_DIR="$tmp/backup"
mkdir "$BACKUP_DIR"
backup_item "$tmp/target" panel
printf 'new\n' >"$tmp/target"
restore_item panel
assert_equal 'rollback восстанавливает файл' old "$(tr -d '\n' <"$tmp/target")"

backup_item "$tmp/missing" config
printf 'created\n' >"$tmp/missing"
restore_item config
assert_failure 'rollback удаляет новый файл' test -e "$tmp/missing"

mock_systemctl="$tmp/systemctl"
cat >"$mock_systemctl" <<'MOCK'
#!/usr/bin/env bash
case "$1:$2" in
  is-active:awgpanel-routing-dns.service|is-active:awgpanel-routing-xray.service) exit 0 ;;
  is-enabled:awgpanel-routing-dns.service|is-enabled:awgpanel-routing-xray.service) exit 0 ;;
esac
printf '%s\n' "$*" >>"$SYSTEMCTL_LOG"
MOCK
chmod +x "$mock_systemctl"
SYSTEMCTL="$mock_systemctl"
SYSTEMCTL_LOG="$tmp/systemctl.log"
export SYSTEMCTL_LOG
ROUTING_DNS_ACTIVE=0
ROUTING_XRAY_ACTIVE=0
ROUTING_DNS_ENABLED=0
ROUTING_XRAY_ENABLED=0
remember_unit_states
assert_equal 'сохранено active-состояние DNS' 1 "$ROUTING_DNS_ACTIVE"
assert_equal 'сохранено active-состояние Xray' 1 "$ROUTING_XRAY_ACTIVE"
restore_unit_states
assert_success 'routing DNS запускается при восстановлении' grep -Fq 'start awgpanel-routing-dns.service' "$SYSTEMCTL_LOG"
assert_success 'routing Xray запускается при восстановлении' grep -Fq 'start awgpanel-routing-xray.service' "$SYSTEMCTL_LOG"

if command -v sha256sum >/dev/null 2>&1; then
  printf 'release\n' >"$tmp/asset"
  sum="$(sha256sum "$tmp/asset" | awk '{print $1}')"
  printf '%s  awgpanel-linux-amd64\n' "$sum" >"$tmp/SHA256SUMS"
  assert_success 'валидная SHA256-сумма' verify_release_asset "$tmp/SHA256SUMS" awgpanel-linux-amd64 "$tmp/asset"
  printf 'damaged\n' >"$tmp/asset"
  assert_failure 'повреждённый release asset' bash -c "AWGPANEL_INSTALL_LIB_ONLY=1 source '$PROJECT_DIR/install.sh'; verify_release_asset '$tmp/SHA256SUMS' awgpanel-linux-amd64 '$tmp/asset' >/dev/null 2>&1"
fi

printf '1..%d\n' "$tests"
[[ "$failures" -eq 0 ]]
