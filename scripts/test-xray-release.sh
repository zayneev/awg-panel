#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export AWGPANEL_INSTALL_LIB_ONLY=1
# shellcheck source=../install.sh
source "$PROJECT_DIR/install.sh"

for command in curl sha256sum unzip qemu-aarch64-static; do
  command -v "$command" >/dev/null 2>&1 || {
    printf 'Требуется команда %s\n' "$command" >&2
    exit 1
  }
done

work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT

check_archive() {
  local arch="$1" runner pair asset expected archive actual
  pair="$(xray_asset_and_sha "$arch")"
  asset="${pair%% *}"
  expected="${pair##* }"
  archive="$work/$asset"
  curl -fsSL --retry 3 --connect-timeout 15 --proto '=https' --tlsv1.2 \
    "https://github.com/XTLS/Xray-core/releases/download/v${XRAY_VERSION}/${asset}" -o "$archive"
  printf '%s  %s\n' "$expected" "$archive" | sha256sum -c - >/dev/null
  mkdir "$work/$arch"
  unzip -q "$archive" -d "$work/$arch"
  for required in xray geosite.dat geoip.dat; do
    [[ -f "$work/$arch/$required" ]] || {
      printf 'В %s отсутствует %s\n' "$asset" "$required" >&2
      return 1
    }
  done
  runner="$work/$arch/xray"
  if [[ "$arch" == arm64 ]]; then
    export AWGPANEL_TEST_XRAY_ARM64="$runner"
    runner="$work/xray-arm64-runner"
    cat >"$runner" <<'RUNNER'
#!/usr/bin/env bash
exec qemu-aarch64-static "$AWGPANEL_TEST_XRAY_ARM64" "$@"
RUNNER
    chmod +x "$runner"
  fi
  actual="$(verify_xray_binary_version "$runner" "$XRAY_VERSION")" || {
    printf 'Архив %s содержит неожиданную версию Xray: %s\n' "$asset" "${actual:-не определена}" >&2
    return 1
  }
  printf 'ok - %s: Xray %s, SHA256 проверен\n' "$arch" "$actual"
}

check_archive amd64
check_archive arm64
