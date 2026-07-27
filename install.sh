#!/usr/bin/env bash
set -Eeuo pipefail

REPOSITORY="${AWGPANEL_REPOSITORY:-zayneev/awg-panel}"
XRAY_VERSION="26.7.11"
SUPPORTED_AWG_MINORS=("5.20" "5.21")

PANEL_BINARY="${PANEL_BINARY:-}"
REQUESTED_VERSION=""
WITH_ROUTING=0
ROUTING_EXPLICIT=0
XRAY_ARCHIVE=""
PURGE_LEGACY=0
INTERACTIVE_REQUESTED=0
NON_INTERACTIVE_REQUESTED=0
SHOW_HELP=0
ARG_COUNT=0

# AWGPANEL_INSTALL_ROOT is intentionally available only to the test harness.
INSTALL_ROOT="${AWGPANEL_INSTALL_ROOT:-}"
TESTING="${AWGPANEL_INSTALL_TESTING:-0}"
SYSTEMCTL="${AWGPANEL_SYSTEMCTL:-systemctl}"
TRANSACTION_ACTIVE=0
ROLLBACK_RUNNING=0
WORK_TMP=""
BACKUP_DIR=""
ROUTING_WAS_INSTALLED=0
ROUTING_DNS_ACTIVE=0
ROUTING_XRAY_ACTIVE=0
ROUTING_DNS_ENABLED=0
ROUTING_XRAY_ENABLED=0
AWG_INACTIVE=0
TTY_FD=""

root_path() {
  printf '%s%s' "$INSTALL_ROOT" "$1"
}

CONFIG_PATH="$(root_path /etc/awgpanel/config.json)"
PANEL_TARGET="$(root_path /usr/local/bin/awgpanel)"
XRAY_TARGET="$(root_path /usr/local/lib/awgpanel/xray)"
XRAY_ASSETS_DIR="$(root_path /usr/local/share/awgpanel/xray)"
ROUTING_DIR="$(root_path /etc/awgpanel/routing)"
DNS_UNIT_PATH="$(root_path /etc/systemd/system/awgpanel-routing-dns.service)"
XRAY_UNIT_PATH="$(root_path /etc/systemd/system/awgpanel-routing-xray.service)"
MANAGE_SCRIPT="$(root_path /root/awg/manage_amneziawg.sh)"
COMMON_SCRIPT="$(root_path /root/awg/awg_common.sh)"
SERVER_CONFIG="$(root_path /etc/amnezia/amneziawg/awg0.conf)"

usage() {
  cat <<'USAGE'
Использование:
  sudo ./install.sh                         интерактивный мастер
  sudo ./install.sh [опции]                 автоматическая установка

Опции:
  --binary=PATH                 локальный бинарник (офлайн-режим)
  --version=VERSION             версия GitHub-релиза, например 0.3.0
  --with-routing                установить компоненты WARP routing
  --xray-archive=PATH           локальный официальный архив Xray
  --purge-legacy-web-state      удалить сохранённые данные старой web-панели
  --interactive                 принудительно открыть мастер
  --non-interactive             запретить любые вопросы
  -h, --help                    показать справку

Без --binary установщик скачивает последний релиз для amd64 или arm64 и
проверяет его по SHA256SUMS. Существующие конфиги и WARP-секреты сохраняются.
USAGE
}

die() {
  if [[ -t 2 && "${TERM:-dumb}" != "dumb" ]]; then
    printf '\033[1;31mОшибка:\033[0m %s\n' "$*" >&2
  else
    printf 'Ошибка: %s\n' "$*" >&2
  fi
  exit 1
}

info() {
  if [[ -t 1 && "${TERM:-dumb}" != "dumb" ]]; then
    printf '\n\033[1;36m==> %s\033[0m\n' "$*"
  else
    printf '\n==> %s\n' "$*"
  fi
}

warn() {
  if [[ -t 2 && "${TERM:-dumb}" != "dumb" ]]; then
    printf '\033[1;33mПредупреждение:\033[0m %s\n' "$*" >&2
  else
    printf 'Предупреждение: %s\n' "$*" >&2
  fi
}

parse_args() {
  ARG_COUNT=$#
  local arg
  for arg in "$@"; do
    case "$arg" in
      --binary=*) PANEL_BINARY="${arg#*=}" ;;
      --version=*) REQUESTED_VERSION="${arg#*=}" ;;
      --with-routing) WITH_ROUTING=1; ROUTING_EXPLICIT=1 ;;
      --xray-archive=*) XRAY_ARCHIVE="${arg#*=}" ;;
      --purge-legacy-web-state) PURGE_LEGACY=1 ;;
      --interactive) INTERACTIVE_REQUESTED=1 ;;
      --non-interactive) NON_INTERACTIVE_REQUESTED=1 ;;
      -h|--help) SHOW_HELP=1 ;;
      *) printf 'Неизвестная опция: %s\n' "$arg" >&2; return 2 ;;
    esac
  done
  [[ "$INTERACTIVE_REQUESTED" -eq 0 || "$NON_INTERACTIVE_REQUESTED" -eq 0 ]] || {
    printf '%s\n' '--interactive и --non-interactive нельзя использовать вместе.' >&2
    return 2
  }
  [[ -z "$XRAY_ARCHIVE" || "$WITH_ROUTING" -eq 1 ]] || {
    printf '%s\n' '--xray-archive используется только вместе с --with-routing.' >&2
    return 2
  }
  [[ -z "$PANEL_BINARY" || -z "$REQUESTED_VERSION" ]] || {
    printf '%s\n' '--binary и --version являются взаимоисключающими.' >&2
    return 2
  }
  if [[ -n "$REQUESTED_VERSION" ]]; then
    REQUESTED_VERSION="${REQUESTED_VERSION#v}"
    [[ "$REQUESTED_VERSION" =~ ^[0-9A-Za-z][0-9A-Za-z._-]*$ ]] || {
      printf 'Некорректная версия: %s\n' "$REQUESTED_VERSION" >&2
      return 2
    }
  fi
}

machine_arch() {
  local machine="${1:-$(uname -m)}"
  case "$machine" in
    x86_64|amd64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    *) return 1 ;;
  esac
}

panel_asset() {
  printf 'awgpanel-linux-%s\n' "$(machine_arch "${1:-$(uname -m)}")"
}

release_base_url() {
  local version="${1:-}"
  if [[ -n "$version" ]]; then
    printf 'https://github.com/%s/releases/download/v%s\n' "$REPOSITORY" "$version"
  else
    printf 'https://github.com/%s/releases/latest/download\n' "$REPOSITORY"
  fi
}

has_tty() {
  [[ -r /dev/tty && -w /dev/tty ]]
}

open_tty() {
  [[ -n "$TTY_FD" ]] && return 0
  exec 3<>/dev/tty
  TTY_FD=3
}

ask_yes_no() {
  local prompt="$1" default="${2:-no}" answer suffix
  [[ "$default" == "yes" ]] && suffix='[Y/n]' || suffix='[y/N]'
  printf '%s %s ' "$prompt" "$suffix" >&$TTY_FD
  IFS= read -r answer <&$TTY_FD || answer=""
  answer="$(printf '%s' "$answer" | tr '[:upper:]' '[:lower:]')"
  case "$answer" in
    y|yes|д|да|Д|ДА) return 0 ;;
    n|no|н|нет|Н|НЕТ) return 1 ;;
    '') [[ "$default" == "yes" ]] ;;
    *) printf 'Введите «да» или «нет».\n' >&$TTY_FD; ask_yes_no "$prompt" "$default" ;;
  esac
}

legacy_data_exists() {
  [[ -e "$(root_path /etc/awg-panel/secret.key)" || -e "$(root_path /var/lib/awg-panel/panel.db)" ]]
}

routing_installed() {
  [[ -e "$DNS_UNIT_PATH" || -e "$XRAY_UNIT_PATH" || -e "$XRAY_TARGET" ]]
}

routing_active() {
  command -v "$SYSTEMCTL" >/dev/null 2>&1 || return 1
  "$SYSTEMCTL" is-active --quiet awgpanel-routing-dns.service 2>/dev/null ||
    "$SYSTEMCTL" is-active --quiet awgpanel-routing-xray.service 2>/dev/null
}

include_active_routing() {
  if routing_active && [[ "$WITH_ROUTING" -eq 0 ]]; then
    WITH_ROUTING=1
    ROUTING_EXPLICIT=1
    warn "обнаружен активный routing; он будет обновлён и безопасно перезапущен"
  fi
}

run_wizard() {
  open_tty || die "интерактивный режим требует TTY; используйте --non-interactive"
  if [[ "${TERM:-dumb}" != "dumb" ]]; then printf '\n\033[1;36mAWG Panel — мастер установки\033[0m\n' >&$TTY_FD; else printf '\nAWG Panel — мастер установки\n' >&$TTY_FD; fi
  printf '─────────────────────────────\n' >&$TTY_FD
  printf 'Панель управляет существующей AmneziaWG 5.20.x/5.21.x и не переустанавливает VPN.\n\n' >&$TTY_FD

  if [[ "$ROUTING_EXPLICIT" -eq 0 ]]; then
    if routing_installed; then
      if ask_yes_no 'Обновить уже установленные компоненты WARP routing?' yes; then WITH_ROUTING=1; else WITH_ROUTING=0; fi
    else
      if ask_yes_no 'Установить дополнительные компоненты WARP routing?' no; then WITH_ROUTING=1; else WITH_ROUTING=0; fi
    fi
  fi
  if legacy_data_exists && [[ "$PURGE_LEGACY" -eq 0 ]]; then
    if ask_yes_no 'Удалить секреты и базу старой web-панели после успешной установки?' no; then PURGE_LEGACY=1; fi
  fi

  printf '\nСводка:\n' >&$TTY_FD
  if [[ -n "$PANEL_BINARY" ]]; then
    printf '  Источник панели: локальный файл %s\n' "$PANEL_BINARY" >&$TTY_FD
  elif [[ -n "$REQUESTED_VERSION" ]]; then
    printf '  Источник панели: GitHub Release v%s\n' "$REQUESTED_VERSION" >&$TTY_FD
  else
    printf '  Источник панели: последний GitHub Release\n' >&$TTY_FD
  fi
  [[ "$WITH_ROUTING" -eq 1 ]] && printf '  Routing: установить или обновить\n' >&$TTY_FD || printf '  Routing: не изменять\n' >&$TTY_FD
  [[ "$PURGE_LEGACY" -eq 1 ]] && printf '  Старые web-данные: удалить после проверки\n' >&$TTY_FD || printf '  Старые web-данные: сохранить\n' >&$TTY_FD
  routing_installed && printf '  Обновление: с резервной копией и автоматическим откатом\n' >&$TTY_FD
  printf '\n' >&$TTY_FD
  ask_yes_no 'Начать установку?' no || { printf 'Установка отменена.\n' >&$TTY_FD; exit 0; }
}

need_command() {
  command -v "$1" >/dev/null 2>&1 || die "не найдена обязательная команда: $1"
}

extract_script_version() {
  local file="$1" variable="$2"
  sed -nE "s/^[[:space:]]*${variable}=[\"']?([^\"'[:space:]]+).*/\\1/p" "$file" | head -n 1
}

version_minor() {
  local version="$1"
  if [[ "$version" =~ ^([0-9]+)\.([0-9]+)\. ]]; then
    printf '%s.%s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
    return 0
  fi
  return 1
}

is_supported_awg_minor() {
  local value="$1" supported
  for supported in "${SUPPORTED_AWG_MINORS[@]}"; do
    [[ "$value" == "$supported" ]] && return 0
  done
  return 1
}

supported_awg_versions() {
  local supported result=""
  for supported in "${SUPPORTED_AWG_MINORS[@]}"; do
    [[ -z "$result" ]] || result+=", "
    result+="${supported}.x"
  done
  printf '%s\n' "$result"
}

check_awg_compatibility() {
  local manage_version common_version manage_minor common_minor
  manage_version="$(extract_script_version "$MANAGE_SCRIPT" SCRIPT_VERSION)"
  common_version="$(extract_script_version "$COMMON_SCRIPT" AWG_COMMON_VERSION)"
  [[ -n "$manage_version" ]] || die "не удалось определить версию $MANAGE_SCRIPT"
  [[ -n "$common_version" ]] || die "не удалось определить версию $COMMON_SCRIPT"
  manage_minor="$(version_minor "$manage_version")" || die "неверный формат версии AmneziaWG: $manage_version"
  common_minor="$(version_minor "$common_version")" || die "неверный формат версии awg_common: $common_version"
  [[ "$manage_minor" == "$common_minor" ]] || die "версии manage ($manage_version) и awg_common ($common_version) несовместимы"
  is_supported_awg_minor "$manage_minor" || die "поддерживаются AmneziaWG $(supported_awg_versions); обнаружена $manage_version"
  printf '  AmneziaWG: manage %s, common %s\n' "$manage_version" "$common_version"
}

check_routing_platform() {
  [[ -r "$(root_path /etc/os-release)" ]] || die "не удалось определить ОС для routing"
  local id version_id
  id="$(sed -nE 's/^ID=(.*)$/\1/p' "$(root_path /etc/os-release)" | tr -d '"')"
  version_id="$(sed -nE 's/^VERSION_ID=(.*)$/\1/p' "$(root_path /etc/os-release)" | tr -d '"')"
  case "$id:$version_id" in
    debian:12|ubuntu:22.04|ubuntu:24.04) ;;
    *) die "routing поддерживается на Debian 12 и Ubuntu 22.04/24.04; обнаружено $id $version_id" ;;
  esac
}

preflight() {
  info "Проверка сервера"
  if [[ -n "$INSTALL_ROOT" && "$TESTING" != "1" ]]; then
    die "AWGPANEL_INSTALL_ROOT разрешён только тестовому harness"
  fi
  if [[ "$TESTING" != "1" ]]; then
    [[ "$(id -u)" -eq 0 ]] || die "запустите установщик через sudo"
    [[ "$(uname -s)" == "Linux" ]] || die "поддерживается только Linux"
  fi
  machine_arch >/dev/null || die "поддерживаются только amd64 и arm64"
  need_command install
  need_command mktemp
  need_command sed
  need_command "$SYSTEMCTL"
  [[ -e "$MANAGE_SCRIPT" ]] || die "не найден установленный AmneziaWG: $MANAGE_SCRIPT"
  [[ -e "$COMMON_SCRIPT" ]] || die "не найден установленный AmneziaWG: $COMMON_SCRIPT"
  [[ -e "$SERVER_CONFIG" ]] || die "не найден конфиг AmneziaWG: $SERVER_CONFIG"
  check_awg_compatibility
  if [[ -z "$PANEL_BINARY" ]]; then
    need_command curl
    need_command sha256sum
  else
    [[ -f "$PANEL_BINARY" && -x "$PANEL_BINARY" ]] || die "бинарник не найден или не исполняемый: $PANEL_BINARY"
  fi
  if [[ "$WITH_ROUTING" -eq 1 ]]; then
    check_routing_platform
    need_command sha256sum
    [[ -z "$XRAY_ARCHIVE" || -f "$XRAY_ARCHIVE" ]] || die "архив Xray не найден: $XRAY_ARCHIVE"
    local dns_active=0 xray_active=0
    "$SYSTEMCTL" is-active --quiet awgpanel-routing-dns.service 2>/dev/null && dns_active=1 || true
    "$SYSTEMCTL" is-active --quiet awgpanel-routing-xray.service 2>/dev/null && xray_active=1 || true
    [[ "$dns_active" -eq "$xray_active" ]] || die "routing уже находится в неполном состоянии; сначала выполните sudo awgpanel routing emergency-disable --yes и устраните причину"
  fi
  if "$SYSTEMCTL" is-active --quiet awg-quick@awg0.service 2>/dev/null; then
    printf '  awg0: активен\n'
  else
    AWG_INACTIVE=1
    warn "awg0 сейчас не активен; панель будет установлена, но VPN требует отдельной проверки"
  fi
}

early_preflight() {
  if [[ -n "$INSTALL_ROOT" && "$TESTING" != "1" ]]; then
    die "AWGPANEL_INSTALL_ROOT разрешён только тестовому harness"
  fi
  if [[ "$TESTING" != "1" ]]; then
    [[ "$(id -u)" -eq 0 ]] || die "запустите установщик через sudo"
    [[ "$(uname -s)" == "Linux" ]] || die "поддерживается только Linux"
  fi
  need_command "$SYSTEMCTL"
  include_active_routing
}

xray_asset_and_sha() {
  case "$(machine_arch)" in
    amd64) printf '%s %s\n' 'Xray-linux-64.zip' 'aa11c3685c71da0ffc71e511db50404609e7e963bb914b048f59a6a00af8930e' ;;
    arm64) printf '%s %s\n' 'Xray-linux-arm64-v8a.zip' '89cfe01674d7c9f6847b7dd9389537be9acb3b9dc3c6cb9fdeba87a3e4e57fc1' ;;
  esac
}

download() {
  local url="$1" target="$2"
  curl -fL --retry 3 --connect-timeout 15 --proto '=https' --tlsv1.2 "$url" -o "$target"
}

verify_release_asset() {
  local sums="$1" asset="$2" file="$3" expected count actual
  count="$(awk -v name="$asset" '$2 == name || $2 == "*" name { count++ } END { print count+0 }' "$sums")"
  [[ "$count" -eq 1 ]] || die "в SHA256SUMS отсутствует однозначная запись для $asset"
  expected="$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1 }' "$sums")"
  [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || die "некорректная SHA256-сумма для $asset"
  actual="$(sha256sum "$file" | awk '{print $1}')"
  actual="$(printf '%s' "$actual" | tr 'A-F' 'a-f')"
  expected="$(printf '%s' "$expected" | tr 'A-F' 'a-f')"
  [[ "$actual" == "$expected" ]] || die "SHA256 не совпадает для $asset"
}

stage_panel() {
  local staged="$WORK_TMP/awgpanel" asset base sums
  if [[ -n "$PANEL_BINARY" ]]; then
    install -m 0755 "$PANEL_BINARY" "$staged"
  else
    asset="$(panel_asset)"
    base="$(release_base_url "$REQUESTED_VERSION")"
    sums="$WORK_TMP/SHA256SUMS"
    info "Загрузка AWG Panel${REQUESTED_VERSION:+ v$REQUESTED_VERSION}"
    download "$base/SHA256SUMS" "$sums"
    download "$base/$asset" "$staged"
    verify_release_asset "$sums" "$asset" "$staged"
    chmod 0755 "$staged"
  fi
  local version_output
  version_output="$("$staged" --version 2>&1)" || die "скачанный бинарник awgpanel не запускается"
  [[ "$version_output" == awgpanel\ version\ * ]] || die "файл не похож на awgpanel: $version_output"
  if [[ -n "$REQUESTED_VERSION" ]]; then
    [[ "$version_output" == "awgpanel version $REQUESTED_VERSION" ]] || die "ожидалась версия $REQUESTED_VERSION, получено: $version_output"
  fi
  printf '  Проверен %s\n' "$version_output"
}

validate_existing_config() {
  [[ -e "$CONFIG_PATH" ]] || return 0
  local status_json classification
  status_json="$("$WORK_TMP/awgpanel" status --config "$CONFIG_PATH" --json 2>/dev/null || true)"
  classify_status_json "$status_json" || classification=$?
  classification="${classification:-0}"
  [[ "$classification" -ne 2 ]] || die "существующий $CONFIG_PATH некорректен или несовместим с новой версией"
  printf '  Существующий config.json проверен и будет сохранён.\n'
}

stage_routing() {
  [[ "$WITH_ROUTING" -eq 1 ]] || return 0
  local pair xray_asset xray_sha archive
  pair="$(xray_asset_and_sha)"
  xray_asset="${pair%% *}"
  xray_sha="${pair##* }"
  archive="$WORK_TMP/$xray_asset"
  info "Подготовка Xray v$XRAY_VERSION"
  if [[ -n "$XRAY_ARCHIVE" ]]; then
    install -m 0600 "$XRAY_ARCHIVE" "$archive"
  else
    download "https://github.com/XTLS/Xray-core/releases/download/v${XRAY_VERSION}/${xray_asset}" "$archive"
  fi
  printf '%s  %s\n' "$xray_sha" "$archive" | sha256sum -c - >/dev/null
  mkdir "$WORK_TMP/xray"
  unzip -q "$archive" -d "$WORK_TMP/xray"
  local required
  for required in xray geosite.dat geoip.dat; do
    [[ -f "$WORK_TMP/xray/$required" ]] || die "в архиве Xray отсутствует $required"
  done
  "$WORK_TMP/xray/xray" version | grep -Fq "Xray $XRAY_VERSION" || die "архив содержит неожиданную версию Xray"
}

write_default_config() {
  local target="$1"
  cat >"$target" <<'JSON'
{
  "manageScript": "/root/awg/manage_amneziawg.sh",
  "commonScript": "/root/awg/awg_common.sh",
  "awgDir": "/root/awg",
  "serverConfig": "/etc/amnezia/amneziawg/awg0.conf",
  "routingDir": "/etc/awgpanel/routing",
  "routingConfig": "/etc/awgpanel/routing/routing.json",
  "warpSecrets": "/etc/awgpanel/routing/warp.json",
  "xrayBinary": "/usr/local/lib/awgpanel/xray",
  "xrayAssets": "/usr/local/share/awgpanel/xray",
  "xrayConfig": "/etc/awgpanel/routing/xray.json",
  "geoSiteData": "/usr/local/share/awgpanel/xray/geosite.dat",
  "routingInterface": "awg0",
  "dnsListen": "0.0.0.0",
  "dnsPort": 1053,
  "tproxyPort": 17890,
  "healthPort": 17891,
  "fwMark": 2657,
  "routeTable": 1061
}
JSON
}

write_units() {
  cat >"$WORK_TMP/awgpanel-routing-dns.service" <<'UNIT'
[Unit]
Description=AWG Panel domain DNS classifier
After=network-online.target awg-quick@awg0.service awgpanel-routing-xray.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/awgpanel routing internal-dns
ExecStartPost=/usr/local/bin/awgpanel routing internal-recover
ExecStopPost=-/usr/local/bin/awgpanel routing internal-remove-intercept
Restart=on-failure
RestartSec=2s
User=root
Group=root
UMask=0077
AmbientCapabilities=CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_ADMIN
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/etc/awgpanel/routing
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true

[Install]
WantedBy=multi-user.target
UNIT
  cat >"$WORK_TMP/awgpanel-routing-xray.service" <<'UNIT'
[Unit]
Description=AWG Panel isolated Xray WARP outbound
After=network-online.target awg-quick@awg0.service
Wants=network-online.target

[Service]
Type=simple
Environment=XRAY_LOCATION_ASSET=/usr/local/share/awgpanel/xray
ExecStart=/usr/local/lib/awgpanel/xray run -config /etc/awgpanel/routing/xray.json
ExecStartPost=/usr/local/bin/awgpanel routing internal-recover
ExecStopPost=-/usr/local/bin/awgpanel routing internal-remove-intercept
Restart=on-failure
RestartSec=2s
User=root
Group=root
UMask=0077
AmbientCapabilities=CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_ADMIN
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/etc/awgpanel/routing
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true

[Install]
WantedBy=multi-user.target
UNIT
}

install_owned() {
  local mode="$1" source="$2" target="$3"
  if [[ "$TESTING" == "1" ]]; then
    install -m "$mode" "$source" "$target"
  else
    install -m "$mode" -o root -g root "$source" "$target"
  fi
}

install_dir() {
  local mode="$1" target="$2"
  if [[ "$TESTING" == "1" ]]; then
    install -d -m "$mode" "$target"
  else
    install -d -m "$mode" -o root -g root "$target"
  fi
}

backup_item() {
  local target="$1" key="$2"
  printf '%s\n' "$target" >"$BACKUP_DIR/$key.target"
  if [[ -e "$target" || -L "$target" ]]; then
    cp -a "$target" "$BACKUP_DIR/$key.data"
    : >"$BACKUP_DIR/$key.present"
  fi
}

restore_item() {
  local key="$1" target
  target="$(<"$BACKUP_DIR/$key.target")"
  [[ -n "$target" && "$target" != "/" && "$target" != "$INSTALL_ROOT" ]] || return 1
  rm -rf -- "$target"
  if [[ -e "$BACKUP_DIR/$key.present" ]]; then
    mkdir -p "$(dirname "$target")"
    cp -a "$BACKUP_DIR/$key.data" "$target"
  fi
}

remember_unit_states() {
  routing_installed && ROUTING_WAS_INSTALLED=1
  "$SYSTEMCTL" is-active --quiet awgpanel-routing-dns.service 2>/dev/null && ROUTING_DNS_ACTIVE=1 || true
  "$SYSTEMCTL" is-active --quiet awgpanel-routing-xray.service 2>/dev/null && ROUTING_XRAY_ACTIVE=1 || true
  "$SYSTEMCTL" is-enabled --quiet awgpanel-routing-dns.service 2>/dev/null && ROUTING_DNS_ENABLED=1 || true
  "$SYSTEMCTL" is-enabled --quiet awgpanel-routing-xray.service 2>/dev/null && ROUTING_XRAY_ENABLED=1 || true
}

restore_unit_states() {
  "$SYSTEMCTL" daemon-reload >/dev/null 2>&1 || true
  if [[ "$ROUTING_DNS_ENABLED" -eq 1 ]]; then "$SYSTEMCTL" enable awgpanel-routing-dns.service >/dev/null 2>&1 || true; else "$SYSTEMCTL" disable awgpanel-routing-dns.service >/dev/null 2>&1 || true; fi
  if [[ "$ROUTING_XRAY_ENABLED" -eq 1 ]]; then "$SYSTEMCTL" enable awgpanel-routing-xray.service >/dev/null 2>&1 || true; else "$SYSTEMCTL" disable awgpanel-routing-xray.service >/dev/null 2>&1 || true; fi
  if [[ "$ROUTING_XRAY_ACTIVE" -eq 1 ]]; then "$SYSTEMCTL" start awgpanel-routing-xray.service >/dev/null 2>&1 || true; else "$SYSTEMCTL" stop awgpanel-routing-xray.service >/dev/null 2>&1 || true; fi
  if [[ "$ROUTING_DNS_ACTIVE" -eq 1 ]]; then "$SYSTEMCTL" start awgpanel-routing-dns.service >/dev/null 2>&1 || true; else "$SYSTEMCTL" stop awgpanel-routing-dns.service >/dev/null 2>&1 || true; fi
}

rollback() {
  local status="${1:-1}"
  [[ "$TRANSACTION_ACTIVE" -eq 1 && "$ROLLBACK_RUNNING" -eq 0 ]] || return "$status"
  ROLLBACK_RUNNING=1
  warn "установка не завершена; восстанавливаю предыдущую версию"
  "$PANEL_TARGET" routing emergency-disable --yes >/dev/null 2>&1 || true
  "$SYSTEMCTL" stop awgpanel-routing-dns.service awgpanel-routing-xray.service >/dev/null 2>&1 || true
  local key
  for key in panel config xray assets dns-unit xray-unit; do
    [[ -e "$BACKUP_DIR/$key.target" ]] && restore_item "$key" || true
  done
  restore_unit_states
  if [[ "$ROUTING_XRAY_ACTIVE" -eq 1 ]] && ! "$SYSTEMCTL" is-active --quiet awgpanel-routing-xray.service 2>/dev/null; then
    warn "файлы Xray восстановлены, но routing-служба не запустилась; проверьте systemctl status awgpanel-routing-xray"
  fi
  if [[ "$ROUTING_DNS_ACTIVE" -eq 1 ]] && ! "$SYSTEMCTL" is-active --quiet awgpanel-routing-dns.service 2>/dev/null; then
    warn "файлы DNS-классификатора восстановлены, но служба не запустилась; проверьте systemctl status awgpanel-routing-dns"
  fi
  TRANSACTION_ACTIVE=0
  warn "откат завершён"
  return "$status"
}

on_error() {
  local status=$?
  rollback "$status" || true
  exit "$status"
}

on_exit() {
  local status=$?
  if [[ "$status" -ne 0 ]]; then rollback "$status" || true; fi
  [[ -z "$WORK_TMP" ]] || rm -rf -- "$WORK_TMP"
}

begin_transaction() {
  BACKUP_DIR="$WORK_TMP/backup"
  mkdir "$BACKUP_DIR"
  remember_unit_states
  backup_item "$PANEL_TARGET" panel
  backup_item "$CONFIG_PATH" config
  backup_item "$XRAY_TARGET" xray
  backup_item "$XRAY_ASSETS_DIR" assets
  backup_item "$DNS_UNIT_PATH" dns-unit
  backup_item "$XRAY_UNIT_PATH" xray-unit
  TRANSACTION_ACTIVE=1
}

install_dependencies() {
  [[ "$WITH_ROUTING" -eq 1 ]] || return 0
  if command -v unzip >/dev/null 2>&1 && command -v nft >/dev/null 2>&1 && command -v ip >/dev/null 2>&1; then
    printf '  Зависимости routing уже установлены; APT и сеть не требуются.\n'
    return 0
  fi
  info "Установка системных зависимостей routing"
  need_command apt-get
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends ca-certificates curl nftables unzip
}

install_files() {
  info "Установка файлов"
  if [[ "$WITH_ROUTING" -eq 1 && ( "$ROUTING_DNS_ACTIVE" -eq 1 || "$ROUTING_XRAY_ACTIVE" -eq 1 ) ]]; then
    printf '  Активный routing временно переводится в direct-режим.\n'
    "$PANEL_TARGET" routing emergency-disable --yes >/dev/null 2>&1 || true
    "$SYSTEMCTL" stop awgpanel-routing-dns.service awgpanel-routing-xray.service
  fi
  install_dir 0700 "$(dirname "$CONFIG_PATH")"
  install_dir 0755 "$(dirname "$PANEL_TARGET")"
  install_owned 0755 "$WORK_TMP/awgpanel" "$PANEL_TARGET"
  if [[ ! -e "$CONFIG_PATH" ]]; then
    write_default_config "$WORK_TMP/config.json"
    install_owned 0600 "$WORK_TMP/config.json" "$CONFIG_PATH"
  else
    printf '  Существующий %s сохранён без изменений.\n' "$CONFIG_PATH"
  fi
  if [[ "$WITH_ROUTING" -eq 1 ]]; then
    write_units
    install_dir 0755 "$(dirname "$XRAY_TARGET")"
    install_dir 0755 "$XRAY_ASSETS_DIR"
    install_dir 0700 "$ROUTING_DIR"
    install_dir 0755 "$(dirname "$DNS_UNIT_PATH")"
    install_owned 0755 "$WORK_TMP/xray/xray" "$XRAY_TARGET"
    install_owned 0644 "$WORK_TMP/xray/geosite.dat" "$XRAY_ASSETS_DIR/geosite.dat"
    install_owned 0644 "$WORK_TMP/xray/geoip.dat" "$XRAY_ASSETS_DIR/geoip.dat"
    install_owned 0644 "$WORK_TMP/awgpanel-routing-dns.service" "$DNS_UNIT_PATH"
    install_owned 0644 "$WORK_TMP/awgpanel-routing-xray.service" "$XRAY_UNIT_PATH"
    "$SYSTEMCTL" daemon-reload
    if [[ "$ROUTING_DNS_ENABLED" -eq 1 ]]; then "$SYSTEMCTL" enable awgpanel-routing-dns.service >/dev/null; else "$SYSTEMCTL" disable awgpanel-routing-dns.service >/dev/null 2>&1 || true; fi
    if [[ "$ROUTING_XRAY_ENABLED" -eq 1 ]]; then "$SYSTEMCTL" enable awgpanel-routing-xray.service >/dev/null; else "$SYSTEMCTL" disable awgpanel-routing-xray.service >/dev/null 2>&1 || true; fi
  fi
}

classify_status_json() {
  local json="$1" compact
  compact="$(printf '%s' "$json" | tr -d '[:space:]')"
  if [[ "$compact" != *'"compatibility":{"ok":true'* ]]; then
    return 2
  fi
  [[ "$compact" == *'"serviceActive":true'* ]] && return 0
  return 1
}

validate_installation() {
  info "Проверка установленной панели"
  "$PANEL_TARGET" --version
  local status_json classification
  status_json="$("$PANEL_TARGET" status --config "$CONFIG_PATH" --json 2>/dev/null || true)"
  classify_status_json "$status_json" || classification=$?
  classification="${classification:-0}"
  case "$classification" in
    0) printf '  Совместимость и awg0 проверены.\n' ;;
    1) warn "панель совместима, но awg0 не активен" ;;
    *) printf '%s\n' "$status_json" >&2; die "установленная панель не прошла проверку совместимости" ;;
  esac
  if [[ "$WITH_ROUTING" -eq 1 && ( "$ROUTING_DNS_ACTIVE" -eq 1 || "$ROUTING_XRAY_ACTIVE" -eq 1 ) ]]; then
    info "Восстановление активного routing"
    [[ "$ROUTING_XRAY_ACTIVE" -eq 0 ]] || "$SYSTEMCTL" start awgpanel-routing-xray.service
    [[ "$ROUTING_DNS_ACTIVE" -eq 0 ]] || "$SYSTEMCTL" start awgpanel-routing-dns.service
    local i routing_json
    for i in 1 2 3 4 5; do
      routing_json="$("$PANEL_TARGET" routing status --config "$CONFIG_PATH" --json 2>/dev/null || true)"
      if [[ "$routing_json" == *'"state":"active"'* && "$routing_json" == *'"dnsActive":true'* && "$routing_json" == *'"xrayActive":true'* ]]; then
        printf '  Routing снова активен и прошёл проверку.\n'
        return 0
      fi
      sleep 2
    done
    printf '%s\n' "$routing_json" >&2
    die "routing не восстановился после обновления"
  fi
}

migrate_legacy_web() {
  info "Завершение миграции старой web-панели"
  local unit
  for unit in awg-panel-web.service awg-panel-agent.service; do
    if "$SYSTEMCTL" list-unit-files "$unit" --no-legend 2>/dev/null | grep -Fq "$unit"; then
      "$SYSTEMCTL" disable --now "$unit" >/dev/null 2>&1 || true
    fi
  done
  rm -f -- "$(root_path /etc/systemd/system/awg-panel-web.service)" "$(root_path /etc/systemd/system/awg-panel-agent.service)"
  rm -f -- "$(root_path /usr/local/bin/awg-panel)" "$(root_path /run/awg-panel/agent.sock)"
  "$SYSTEMCTL" daemon-reload || warn "systemd daemon-reload после удаления старых units завершился ошибкой"
  if [[ "$PURGE_LEGACY" -eq 1 ]]; then
    rm -f -- "$(root_path /etc/awg-panel/secret.key)" "$(root_path /etc/awg-panel/config.json)" "$(root_path /var/lib/awg-panel/panel.db)"
    rmdir "$(root_path /etc/awg-panel)" "$(root_path /var/lib/awg-panel)" "$(root_path /run/awg-panel)" 2>/dev/null || true
    if [[ "$TESTING" != "1" ]]; then
      id awg-panel >/dev/null 2>&1 && userdel awg-panel || true
      getent group awg-panel >/dev/null 2>&1 && groupdel awg-panel || true
    fi
  elif legacy_data_exists; then
    warn "данные старой web-панели сохранены; удалить их можно с --purge-legacy-web-state"
  fi
  return 0
}

finish() {
  info "AWG Panel установлена"
  printf '  Бинарник: %s\n' "$PANEL_TARGET"
  printf '  Запуск:   ssh -t <user>@<IP_VPS> '\''sudo awgpanel'\''\n'
  if [[ "$AWG_INACTIVE" -eq 1 ]]; then
    printf '\n  Внимание: awg0 не была активна во время установки.\n'
    printf '  Проверьте: systemctl status awg-quick@awg0\n'
  fi
  if [[ "$WITH_ROUTING" -eq 1 && "$ROUTING_WAS_INSTALLED" -eq 0 ]]; then
    printf '\n  Routing установлен, но выключен; сеть и nftables не изменялись.\n'
    printf '  Следующий шаг: sudo awgpanel routing warp register --accept-tos\n'
    printf '  Затем:         sudo awgpanel routing check\n'
    printf '                 sudo awgpanel routing enable --yes\n'
  fi
  return 0
}

main() {
  parse_args "$@" || { usage >&2; exit 2; }
  if [[ "$SHOW_HELP" -eq 1 ]]; then usage; exit 0; fi
  early_preflight
  if [[ "$INTERACTIVE_REQUESTED" -eq 1 || ( "$ARG_COUNT" -eq 0 && "$NON_INTERACTIVE_REQUESTED" -eq 0 && -r /dev/tty && -w /dev/tty ) ]]; then
    run_wizard
  fi
  preflight
  WORK_TMP="$(mktemp -d)"
  trap on_error ERR
  trap on_exit EXIT
  stage_panel
  validate_existing_config
  install_dependencies
  stage_routing
  begin_transaction
  install_files
  validate_installation
  TRANSACTION_ACTIVE=0
  migrate_legacy_web
  finish
}

if [[ "${AWGPANEL_INSTALL_LIB_ONLY:-0}" != "1" ]]; then
  main "$@"
fi
