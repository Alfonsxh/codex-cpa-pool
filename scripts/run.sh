#!/usr/bin/env sh
set -eu

# CPAP_OPERATOR_PROTOCOL=1
# CPAP_RUNTIME_ROOT_ALIAS=1
# CPAP_RUNTIME_ROOT_CONFIG=1
# CPAP_SNAPSHOT_BACKUP=1
# Compatibility boundary: accept the former operator prefix only as input.
# Values are never evaluated as shell code; the suffix list is fixed here.
for operator_suffix in GITHUB_REPOSITORY RUN_ASSET_URL STAGING_ROOT DEPLOY_ROOT \
  BACKUP_DIR CONFIG_FILE LEGACY_CONFIG_FILE ACME_ROOT NGINX_AVAILABLE_DIRECTORY \
  NGINX_ENABLED_DIRECTORY CERTIFICATE_ROOT ALLOW_NON_ROOT CERTBOT_EMAIL DOMAIN \
  INGRESS_MODE LOCK_FILE; do
  eval "operator_current_set=\${CPAP_${operator_suffix}+set}"
  eval "operator_legacy_set=\${CPAC_${operator_suffix}+set}"
  eval "operator_current_value=\${CPAP_${operator_suffix}-}"
  eval "operator_legacy_value=\${CPAC_${operator_suffix}-}"
  if [ "$operator_current_set" = set ] && [ "$operator_legacy_set" = set ] \
    && [ "$operator_current_value" != "$operator_legacy_value" ]; then
    printf 'ERROR  CPAP_%s 与旧前缀配置冲突，拒绝继续\n' "$operator_suffix" >&2
    exit 1
  fi
  if [ "$operator_current_set" != set ] && [ "$operator_legacy_set" = set ]; then
    export "CPAP_$operator_suffix=$operator_legacy_value"
  fi
done
unset operator_suffix operator_current_set operator_legacy_set operator_current_value operator_legacy_value

# Canonical repository for installation assets and release metadata.
DEFAULT_REPOSITORY=${CPAP_GITHUB_REPOSITORY:-Alfonsxh/codex-cpa-pool}
RUN_ASSET_URL=${CPAP_RUN_ASSET_URL:-https://github.com/$DEFAULT_REPOSITORY/releases/latest/download/run.sh}

# Compatibility boundary: retain existing operator/runtime roots in place.
# An ambiguous pair fails before mkdir, download or script replacement.
resolve_existing_root() {
  current_root=$1
  former_root=$2
  if [ "$current_root" != "$former_root" ] \
    && { [ -e "$former_root" ] || [ -L "$former_root" ]; }; then
    if [ -e "$current_root" ] || [ -L "$current_root" ]; then
      if [ -d "$current_root" ] && [ -d "$former_root" ] \
        && [ "$(CDPATH= cd -- "$current_root" && pwd -P)" = "$(CDPATH= cd -- "$former_root" && pwd -P)" ]; then
        printf '%s\n' "$current_root"
        return 0
      fi
      printf 'ERROR  新旧目录同时存在，请显式指定已有部署目录：%s / %s\n' \
        "$current_root" "$former_root" >&2
      return 1
    fi
    printf '%s\n' "$former_root"
  else
    printf '%s\n' "$current_root"
  fi
}

# Only the operator entry may be an alias. All subsequent writes, re-execs and
# target actions use one physical root; runtime children retain strict checks.
resolve_deploy_root() (
  deploy_path=$1
  case "$deploy_path" in
    /*) ;;
    *) printf 'ERROR  CPAP_DEPLOY_ROOT 必须是绝对路径\n' >&2; return 1 ;;
  esac
  while [ "$deploy_path" != / ] && [ "${deploy_path%/}" != "$deploy_path" ]; do
    deploy_path=${deploy_path%/}
  done
  deploy_suffix=
  # A new installation may have missing parents. Resolve its existing ancestor
  # without creating anything or following a dangling/looping link as a new root.
  while [ ! -e "$deploy_path" ] && [ ! -L "$deploy_path" ]; do
    deploy_component=$(basename -- "$deploy_path")
    case "$deploy_component" in
      .|..) printf 'ERROR  新部署目录不能包含未解析的 . 或 ..：%s\n' "$1" >&2; return 1 ;;
    esac
    deploy_suffix="/$deploy_component$deploy_suffix"
    deploy_path=$(dirname -- "$deploy_path")
  done
  [ -d "$deploy_path" ] || {
    printf 'ERROR  部署入口必须指向有效目录，不能是断链、循环链接或文件：%s\n' "$1" >&2
    return 1
  }
  deploy_physical=$(CDPATH= cd -- "$deploy_path" && pwd -P) || return 1
  deploy_resolved="${deploy_physical%/}$deploy_suffix"
  [ -n "$deploy_resolved" ] || {
    printf 'ERROR  CPAP_DEPLOY_ROOT 不能是文件系统根目录\n' >&2
    return 1
  }
  printf '%s\n' "$deploy_resolved"
)

set_operator_deploy_root() {
  CPAP_DEPLOY_ROOT=$(resolve_deploy_root "$1") || return 1
  export CPAP_DEPLOY_ROOT
  # Keep an explicitly supplied legacy input equal across script replacement.
  if [ "${CPAC_DEPLOY_ROOT+set}" = set ]; then
    CPAC_DEPLOY_ROOT=$CPAP_DEPLOY_ROOT
    export CPAC_DEPLOY_ROOT
  fi
}

# Read only the directory field; operator configuration is never shell code.
config_deploy_root() (
  root_config=$1
  [ -e "$root_config" ] || [ -L "$root_config" ] || return 0
  [ -f "$root_config" ] && [ ! -L "$root_config" ] || {
    printf 'ERROR  运行目录配置必须是普通非符号链接文件：%s\n' "$root_config" >&2
    return 1
  }
  saved_root=$(awk -F= '
    $1 == "CPAP_DEPLOY_ROOT" {
      count++
      value=substr($0, index($0, "=") + 1)
    }
    END {
      if (count > 1 || (count == 1 && value == "")) exit 1
      if (count == 1) print value
    }
  ' "$root_config") || {
    printf 'ERROR  CPAP_DEPLOY_ROOT 配置重复或为空：%s\n' "$root_config" >&2
    return 1
  }
  [ -n "$saved_root" ] || return 0
  resolve_deploy_root "$saved_root"
)

resolve_operator_deploy_root() (
  saved_root=$(config_deploy_root "$1") || return 1
  if [ -n "$saved_root" ]; then
    [ -d "$saved_root" ] || {
      printf 'ERROR  已记录的运行目录不存在，拒绝初始化其他目录：%s\n' "$saved_root" >&2
      return 1
    }
    if [ -n "${CPAP_DEPLOY_ROOT:-}" ]; then
      explicit_root=$(resolve_deploy_root "$CPAP_DEPLOY_ROOT") || return 1
      if [ "$explicit_root" != "$saved_root" ]; then
        printf 'ERROR  CPAP_DEPLOY_ROOT 与已记录的运行目录冲突：%s\n' "$saved_root" >&2
        return 1
      fi
    fi
    printf '%s\n' "$saved_root"
  elif [ -n "${CPAP_DEPLOY_ROOT:-}" ]; then
    resolve_deploy_root "$CPAP_DEPLOY_ROOT"
  else
    selected_root=$(resolve_existing_root "$2" "$3") || return 1
    resolve_deploy_root "$selected_root"
  fi
)

bootstrap_from_stdin() {
  if [ -n "${CPAP_STAGING_ROOT:-}" ]; then
    bootstrap_root=$CPAP_STAGING_ROOT
  else
    bootstrap_root=$(resolve_existing_root /home/ccpa /home/cpac) || exit 1
  fi
  bootstrap_config=${CPAP_CONFIG_FILE:-$bootstrap_root/config.env}
  # Respect the same --config selection when the script arrives through stdin.
  bootstrap_next_is_config=false
  for bootstrap_argument in "$@"; do
    if [ "$bootstrap_next_is_config" = true ]; then
      bootstrap_config=$bootstrap_argument
      bootstrap_next_is_config=false
    elif [ "$bootstrap_argument" = --config ]; then
      bootstrap_next_is_config=true
    fi
  done
  [ "$bootstrap_next_is_config" = false ] || {
    printf 'ERROR  --config 缺少参数\n' >&2
    exit 1
  }
  bootstrap_deploy_root=$(resolve_operator_deploy_root "$bootstrap_config" "$bootstrap_root/runtime" /opt/codex-cpa-cluster) || exit 1
  set_operator_deploy_root "$bootstrap_deploy_root" || exit 1
  case "$bootstrap_root" in
    /*) ;;
    *) printf 'ERROR  CPAP_STAGING_ROOT 必须是绝对路径\n' >&2; exit 1 ;;
  esac
  [ "$bootstrap_root" != / ] || {
    printf 'ERROR  CPAP_STAGING_ROOT 不能是文件系统根目录\n' >&2
    exit 1
  }
  if [ "$(id -u)" -ne 0 ] && [ "${CPAP_ALLOW_NON_ROOT:-false}" != true ]; then
    printf 'ERROR  请通过 sudo 运行安装脚本\n' >&2
    exit 1
  fi
  command -v curl >/dev/null 2>&1 || {
    printf 'ERROR  缺少运行依赖：curl\n' >&2
    exit 1
  }
  if [ -e "$bootstrap_root" ] || [ -L "$bootstrap_root" ]; then
    [ -d "$bootstrap_root" ] && [ ! -L "$bootstrap_root" ] || {
      printf 'ERROR  安装目录必须是普通目录且不能是符号链接\n' >&2
      exit 1
    }
  else
    mkdir -p -- "$bootstrap_root" || {
      printf 'ERROR  无法创建安装目录\n' >&2
      exit 1
    }
    chmod 0755 "$bootstrap_root" || {
      printf 'ERROR  无法设置安装目录权限\n' >&2
      exit 1
    }
  fi

  bootstrap_destination="$bootstrap_root/run.sh"
  if [ -e "$bootstrap_destination" ] || [ -L "$bootstrap_destination" ]; then
    [ -f "$bootstrap_destination" ] && [ ! -L "$bootstrap_destination" ] || {
      printf 'ERROR  已有安装入口不是普通文件，拒绝覆盖\n' >&2
      exit 1
    }
  fi
  bootstrap_temporary=$(mktemp "$bootstrap_root/.run.bootstrap.XXXXXX") || {
    printf 'ERROR  无法创建安装脚本临时文件\n' >&2
    exit 1
  }
  cleanup_stdin_bootstrap() {
    [ -z "$bootstrap_temporary" ] || rm -f -- "$bootstrap_temporary"
  }
  trap cleanup_stdin_bootstrap EXIT HUP INT TERM
  curl -fsSL --retry 3 --connect-timeout 15 --max-time 300 \
    "$RUN_ASSET_URL" -o "$bootstrap_temporary" || {
      printf 'ERROR  下载 run.sh 失败\n' >&2
      exit 1
    }
  [ -s "$bootstrap_temporary" ] && sh -n "$bootstrap_temporary" || {
    printf 'ERROR  下载的 run.sh 无效\n' >&2
    exit 1
  }
  if [ -f "$bootstrap_destination" ] \
    && grep -Fxq '# CPAP_RUNTIME_ROOT_CONFIG=1' "$bootstrap_destination" \
    && ! grep -Fxq '# CPAP_RUNTIME_ROOT_CONFIG=1' "$bootstrap_temporary"; then
    printf '%s\n' '所选安装器不支持已记录的运行目录；保留当前 run.sh' >&2
    rm -f -- "$bootstrap_temporary"
  else
    chmod 0755 "$bootstrap_temporary" \
      && mv -f -- "$bootstrap_temporary" "$bootstrap_destination" || {
        printf 'ERROR  无法安装 run.sh\n' >&2
        exit 1
      }
  fi
  bootstrap_temporary=
  trap - EXIT HUP INT TERM

  if (: </dev/tty) 2>/dev/null; then
    exec "$bootstrap_destination" "$@" </dev/tty
  fi
  exec "$bootstrap_destination" "$@" </dev/null
}

STDIN_BOOTSTRAP=false
case "${0##*/}" in
  sh|dash|bash|ksh|zsh)
    [ -t 0 ] || STDIN_BOOTSTRAP=true
    ;;
esac

SCRIPT_DIRECTORY=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
SCRIPT_PATH="$SCRIPT_DIRECTORY/$(basename -- "$0")"
STAGING_ROOT=${CPAP_STAGING_ROOT:-$SCRIPT_DIRECTORY}
DEFAULT_BACKUP_ROOT=${CPAP_BACKUP_DIR:-$STAGING_ROOT/backups}
DEFAULT_CONFIG_FILE=${CPAP_CONFIG_FILE:-$STAGING_ROOT/config.env}
LEGACY_CONFIG_FILE=${CPAP_LEGACY_CONFIG_FILE:-/etc/cpac/config.env}
ACME_ROOT=${CPAP_ACME_ROOT:-/var/www/letsencrypt}
NGINX_AVAILABLE_DIRECTORY=${CPAP_NGINX_AVAILABLE_DIRECTORY:-/etc/nginx/sites-available}
NGINX_ENABLED_DIRECTORY=${CPAP_NGINX_ENABLED_DIRECTORY:-/etc/nginx/sites-enabled}
CERTIFICATE_ROOT=${CPAP_CERTIFICATE_ROOT:-/etc/letsencrypt/live}

OPERATOR_PUBLIC_PORT=18317

UI_STEP_NUMBER=0
UI_IS_TERMINAL=false
UI_RESET=
UI_BOLD=
UI_DIM=
UI_CYAN=
UI_GREEN=
UI_RED=
UI_YELLOW=
UI_BOX_WIDTH=74

ui_initialize() {
  if [ -t 1 ]; then
    UI_IS_TERMINAL=true
  fi
  if [ "$UI_IS_TERMINAL" = true ] \
    && [ "${TERM:-dumb}" != dumb ] \
    && [ -z "${NO_COLOR+x}" ]; then
    UI_RESET=$(printf '\033[0m')
    UI_BOLD=$(printf '\033[1m')
    UI_DIM=$(printf '\033[2m')
    UI_CYAN=$(printf '\033[36m')
    UI_GREEN=$(printf '\033[32m')
    UI_RED=$(printf '\033[31m')
    UI_YELLOW=$(printf '\033[33m')
  fi
}

ui_banner() {
  if [ "$UI_IS_TERMINAL" = true ]; then
    printf '\n%s%s╭──────────────────────────────────────────────╮%s\n' "$UI_BOLD" "$UI_CYAN" "$UI_RESET"
    printf '%s%s│  CPAP  ·  安装 / 升级 / HTTPS / 健康检查      │%s\n' "$UI_BOLD" "$UI_CYAN" "$UI_RESET"
    printf '%s%s╰──────────────────────────────────────────────╯%s\n' "$UI_BOLD" "$UI_CYAN" "$UI_RESET"
  else
    printf '\n== CPAP 安装与升级 ==\n'
  fi
}

ui_step() {
  UI_STEP_NUMBER=$((UI_STEP_NUMBER + 1))
  if [ "$UI_IS_TERMINAL" = true ]; then
    printf '\n%s◆  %02d%s  %s%s%s\n' \
      "$UI_CYAN" "$UI_STEP_NUMBER" "$UI_RESET" "$UI_BOLD" "$*" "$UI_RESET"
  else
    printf '\n[%02d] %s\n' "$UI_STEP_NUMBER" "$*"
  fi
}

ui_done() {
  if [ "$UI_IS_TERMINAL" = true ]; then
    printf '   %s✓%s  %s\n' "$UI_GREEN" "$UI_RESET" "$*"
  else
    printf '     OK  %s\n' "$*"
  fi
}

ui_note() {
  if [ "$UI_IS_TERMINAL" = true ]; then
    printf '   %s→%s  %s\n' "$UI_DIM" "$UI_RESET" "$*"
  else
    printf '         %s\n' "$*"
  fi
}

ui_error() {
  if [ "$UI_IS_TERMINAL" = true ]; then
    printf '%s✗%s  %s\n' "$UI_RED" "$UI_RESET" "$*" >&2
  else
    printf 'ERROR  %s\n' "$*" >&2
  fi
}

ui_run() {
  ui_title=$1
  shift
  ui_step "$ui_title"
  ui_log=$(mktemp "${TMPDIR:-/var/tmp}/cpap-command.XXXXXX") || {
    ui_error "无法创建阶段日志"
    return 1
  }
  if ( "$@" ) >"$ui_log" 2>&1; then
    if grep -Fq 'Go Gateway force-stopped after drain timeout' "$ui_log"; then
      ui_note "旧 Gateway 连接已超过排空时限，按策略中断并清理"
    fi
    rm -f -- "$ui_log"
    ui_done "$ui_title"
    return 0
  else
    ui_status=$?
  fi
  ui_error "$ui_title 失败"
  cat "$ui_log" >&2
  rm -f -- "$ui_log"
  return "$ui_status"
}

ui_repeat() {
  repeat_character=$1
  repeat_count=$2
  while [ "$repeat_count" -gt 0 ]; do
    printf '%s' "$repeat_character"
    repeat_count=$((repeat_count - 1))
  done
}

ui_display_width() {
  width_text=$1
  width_bytes=$(printf '%s' "$width_text" | wc -c | tr -d '[:space:]')
  width_characters=$(printf '%s' "$width_text" | wc -m | tr -d '[:space:]')
  case "$width_bytes:$width_characters" in
    *[!0-9:]*|:*|*:) width_characters=${#width_text}; width_bytes=$width_characters ;;
  esac
  if [ "$width_bytes" -ge "$width_characters" ]; then
    # Completion rows deliberately use only ASCII plus three-byte CJK text.
    # Each CJK character occupies two terminal cells, so this converts byte
    # and character counts into the displayed width without Python or Perl.
    printf '%s\n' $((width_characters + (width_bytes - width_characters) / 2))
  else
    printf '%s\n' "$width_characters"
  fi
}

ui_box_top() {
  box_title='─ 部署完成 '
  # One box-drawing cell, one space, four double-width CJK characters, one space.
  box_title_width=11
  box_fill=$((UI_BOX_WIDTH - box_title_width))
  [ "$box_fill" -ge 0 ] || box_fill=0
  printf '\n%s%s╭%s' "$UI_BOLD" "$UI_GREEN" "$box_title"
  ui_repeat '─' "$box_fill"
  printf '╮%s\n' "$UI_RESET"
}

ui_box_row() {
  box_text=$1
  box_text_width=$(ui_display_width "$box_text")
  box_padding=$((UI_BOX_WIDTH - box_text_width))
  [ "$box_padding" -ge 0 ] || box_padding=0
  printf '%s%s│%s%s' "$UI_BOLD" "$UI_GREEN" "$UI_RESET" "$box_text"
  printf '%*s' "$box_padding" ''
  printf '%s%s│%s\n' "$UI_BOLD" "$UI_GREEN" "$UI_RESET"
}

ui_box_bottom() {
  printf '%s%s╰' "$UI_BOLD" "$UI_GREEN"
  ui_repeat '─' "$UI_BOX_WIDTH"
  printf '╯%s\n' "$UI_RESET"
}

ui_complete() {
  ui_version=$1
  ui_domain=$2
  ui_root=$3
  ui_ingress_mode=$4
  ui_box_top
  ui_box_row "  版本        ${DEPLOY_SUMMARY_PREVIOUS_VERSION:-未安装} -> $ui_version"
  ui_box_row "  镜像 Control  ${DEPLOY_SUMMARY_CONTROL_IMAGE:-未知}"
  ui_box_row "       Web      ${DEPLOY_SUMMARY_WEB_IMAGE:-未知}"
  ui_box_row "       Gateway  ${DEPLOY_SUMMARY_GATEWAY_IMAGE:-未知}"
  ui_box_row "       Edge     ${DEPLOY_SUMMARY_EDGE_IMAGE:-未知}"
  ui_box_row "  Gateway     ${DEPLOY_SUMMARY_GATEWAY_ACTION:-状态未知}"
  ui_box_row "  核心容器    ${DEPLOY_SUMMARY_CORE_ACTIONS:-状态未知}"
  ui_box_row "  后台任务    ${DEPLOY_SUMMARY_WRITER_ACTIONS:-状态未知}"
  ui_box_row "  备份        ${DEPLOY_SUMMARY_BACKUP:-未创建}"
  ui_box_row "  入口        ${DEPLOY_SUMMARY_INGRESS:-未知}"
  if [ "$ui_ingress_mode" = managed ]; then
    ui_box_row "  地址        https://$ui_domain"
    ui_box_row "  管理员登录  https://$ui_domain/admin/"
  else
    ui_box_row "  入口域名    $ui_domain"
    ui_box_row "  管理员登录  https://$ui_domain/admin/"
  fi
  ui_box_row "  目录        $ui_root"
  ui_box_bottom
}

die() {
  ui_error "$*"
  exit 1
}

ui_initialize

require_root() {
  if [ "$(id -u)" -ne 0 ] && [ "${CPAP_ALLOW_NON_ROOT:-false}" != true ]; then
    die "请使用 sudo 运行：sudo $SCRIPT_PATH $*"
  fi
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "缺少运行依赖：$1"
}

normalize_domain() {
  value=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  value=${value%.}
  [ -n "$value" ] && [ "${#value}" -le 253 ] || return 1
  printf '%s\n' "$value" | awk -F. '
    NF < 2 { exit 1 }
    {
      for (part = 1; part <= NF; part++) {
        label = $part
        if (length(label) < 1 || length(label) > 63 ||
            (label !~ /^[a-z0-9][a-z0-9-]*[a-z0-9]$/ && label !~ /^[a-z0-9]$/)) {
          exit 1
        }
      }
    }
  ' || return 1
  printf '%s\n' "$value"
}

config_domain() {
  config_file=$1
  [ -e "$config_file" ] || return 1
  [ -f "$config_file" ] && [ ! -L "$config_file" ] \
    || die "域名配置必须是普通非符号链接文件：$config_file"
  values=$(awk -F= '$1 == "CPA_DOMAIN" { print substr($0, index($0, "=") + 1) }' "$config_file")
  count=$(printf '%s\n' "$values" | awk 'NF { count++ } END { print count + 0 }')
  [ "$count" -eq 1 ] || die "域名配置必须且只能包含一个 CPA_DOMAIN：$config_file"
  normalize_domain "$values" || die "域名配置无效：$config_file"
}

validate_ingress_mode() {
  case "$1" in
    managed|external) printf '%s\n' "$1" ;;
    *) return 1 ;;
  esac
}

config_ingress_mode() {
  config_file=$1
  [ -e "$config_file" ] || return 1
  [ -f "$config_file" ] && [ ! -L "$config_file" ] \
    || die "入口配置必须是普通非符号链接文件：$config_file"
  # Compatibility boundary: old persisted ingress is input-only.
  values=$(awk -F= '$1 == "CPAP_INGRESS_MODE" || $1 == "CPAC_INGRESS_MODE" {
    if (seen[$1]++) exit 1
    value=substr($0, index($0, "=") + 1)
    if (found++ && value != previous) exit 1
    previous=value
  } END { if (found) print previous }' "$config_file") \
    || die "入口配置的新旧前缀冲突或字段重复：$config_file"
  count=$(printf '%s\n' "$values" | awk 'NF { count++ } END { print count + 0 }')
  case "$count" in
    0) printf '%s\n' managed ;;
    1) validate_ingress_mode "$values" || die "入口模式无效：$config_file" ;;
    *) die "入口配置必须且只能包含一个 CPAP_INGRESS_MODE：$config_file" ;;
  esac
}

config_has_explicit_ingress_mode() {
  config_file=$1
  config_ingress_mode "$config_file" >/dev/null
  count=$(awk -F= '$1 == "CPAP_INGRESS_MODE" || $1 == "CPAC_INGRESS_MODE" { count++ } END { print count + 0 }' "$config_file")
  [ "$count" -ge 1 ]
}

write_config() {
  config_file=$1
  domain=$2
  ingress_mode=${3:-}
  saved_config_root=$(config_deploy_root "$config_file") || die "运行目录配置无效：$config_file"
  config_root=${4:-$saved_config_root}
  if [ -n "$config_root" ]; then
    config_root=$(resolve_deploy_root "$config_root") || die "运行目录配置无效"
  fi
  normalize_domain "$domain" >/dev/null || die "域名配置无效：$domain"
  if [ -n "$ingress_mode" ]; then
    ingress_mode=$(validate_ingress_mode "$ingress_mode") \
      || die "入口模式无效：$ingress_mode"
  fi
  config_directory=$(dirname -- "$config_file")
  [ ! -L "$config_directory" ] || die "配置目录不能是符号链接：$config_directory"
  mkdir -p -- "$config_directory"
  config_tmp=$(mktemp "$config_directory/.config.XXXXXX")
  if ! {
    printf 'CPA_DOMAIN=%s\n' "$domain"
    [ -z "$ingress_mode" ] || printf 'CPAP_INGRESS_MODE=%s\n' "$ingress_mode"
    [ -z "$config_root" ] || printf 'CPAP_DEPLOY_ROOT=%s\n' "$config_root"
  } >"$config_tmp" \
    || ! chmod 0600 "$config_tmp" \
    || ! mv -f -- "$config_tmp" "$config_file"; then
    rm -f -- "$config_tmp"
    die "写入域名配置失败：$config_file"
  fi
}

move_pending_admin_key() {
  pending_source=$1
  pending_destination=$2
  [ -f "$pending_source" ] && [ ! -L "$pending_source" ] \
    || die "旧管理员凭据必须是普通非符号链接文件：$pending_source"
  pending_value=$(awk 'NF { count++; value=$0 } END { if (count == 1) print value; else exit 1 }' "$pending_source") \
    || die "旧管理员凭据内容无效"
  [ "${#pending_value}" -ge 12 ] && [ "${#pending_value}" -le 128 ] \
    || die "旧管理员凭据长度无效"
  [ "$pending_source" != "$pending_destination" ] || return 0
  if [ -e "$pending_destination" ] || [ -L "$pending_destination" ]; then
    [ -f "$pending_destination" ] && [ ! -L "$pending_destination" ] \
      || die "管理员凭据必须是普通非符号链接文件：$pending_destination"
    cmp -s "$pending_source" "$pending_destination" \
      || die "新旧管理员凭据不一致，拒绝自动迁移"
  else
    pending_directory=$(dirname -- "$pending_destination")
    [ ! -L "$pending_directory" ] || die "配置目录不能是符号链接：$pending_directory"
    mkdir -p -- "$pending_directory"
    pending_temporary=$(mktemp "$pending_directory/.bootstrap-admin.XXXXXX")
    if ! cp -- "$pending_source" "$pending_temporary" \
      || ! chmod 0600 "$pending_temporary" \
      || ! cmp -s "$pending_source" "$pending_temporary" \
      || ! mv -f -- "$pending_temporary" "$pending_destination"; then
      rm -f -- "$pending_temporary"
      die "迁移首次管理员凭据失败"
    fi
  fi
  rm -f -- "$pending_source"
}

migrate_legacy_operator_state() {
  migration_destination=$1
  standard_destination="$STAGING_ROOT/config.env"
  [ "$LEGACY_CONFIG_FILE" != "$standard_destination" ] || return 0
  case "$migration_destination" in
    "$standard_destination") ;;
    "$LEGACY_CONFIG_FILE") migration_destination=$standard_destination ;;
    *) return 0 ;;
  esac

  migration_legacy_directory=$(dirname -- "$LEGACY_CONFIG_FILE")
  migration_destination_directory=$(dirname -- "$migration_destination")
  migration_legacy_pending="$migration_legacy_directory/bootstrap-admin.key"
  migration_destination_pending="$migration_destination_directory/bootstrap-admin.key"
  migration_legacy_domain=
  migration_destination_domain=
  migration_changed=false

  if [ -e "$LEGACY_CONFIG_FILE" ] || [ -L "$LEGACY_CONFIG_FILE" ] \
    || [ -e "$migration_legacy_pending" ] || [ -L "$migration_legacy_pending" ]; then
    [ ! -L "$migration_legacy_directory" ] \
      || die "旧配置目录不能是符号链接：$migration_legacy_directory"
  fi
  if [ -e "$LEGACY_CONFIG_FILE" ] || [ -L "$LEGACY_CONFIG_FILE" ]; then
    migration_legacy_domain=$(config_domain "$LEGACY_CONFIG_FILE")
  fi
  if [ -e "$migration_destination" ] || [ -L "$migration_destination" ]; then
    migration_destination_domain=$(config_domain "$migration_destination")
  fi
  if [ -n "$migration_legacy_domain" ] && [ -n "$migration_destination_domain" ] \
    && [ "$migration_legacy_domain" != "$migration_destination_domain" ]; then
    die "新旧域名配置不一致，拒绝自动迁移"
  fi
  if [ -n "$migration_legacy_domain" ] && [ -z "$migration_destination_domain" ]; then
    write_config "$migration_destination" "$migration_legacy_domain" managed
    migration_destination_domain=$migration_legacy_domain
    migration_changed=true
  fi
  if [ -e "$migration_legacy_pending" ] || [ -L "$migration_legacy_pending" ]; then
    move_pending_admin_key "$migration_legacy_pending" "$migration_destination_pending"
    migration_changed=true
  fi
  if [ -n "$migration_legacy_domain" ]; then
    rm -f -- "$LEGACY_CONFIG_FILE"
    migration_changed=true
  fi
  rmdir -- "$migration_legacy_directory" 2>/dev/null || true
  [ "$migration_changed" != true ] \
    || ui_note "已迁移安装配置到 $migration_destination"
  config_file=$migration_destination
}

resolve_deploy_domain() {
  config_file=$1
  explicit_domain=$2
  stored_domain=
  if [ -e "$config_file" ]; then
    stored_domain=$(config_domain "$config_file")
  fi
  if [ -n "$explicit_domain" ]; then
    explicit_domain=$(normalize_domain "$explicit_domain") || die "域名格式无效：$explicit_domain"
    if [ -n "$stored_domain" ] && [ "$stored_domain" != "$explicit_domain" ]; then
      die "已记录域名为 ${stored_domain}；请先使用 sudo $SCRIPT_PATH domain set $explicit_domain"
    fi
    domain=$explicit_domain
  elif [ -n "$stored_domain" ]; then
    domain=$stored_domain
  else
    [ -t 0 ] || die "首次部署必须提供域名：sudo $SCRIPT_PATH run --domain qdata.example.com"
    printf '请输入访问域名: ' >&2
    IFS= read -r entered_domain
    domain=$(normalize_domain "$entered_domain") || die "域名格式无效：$entered_domain"
  fi
  printf '%s\n' "$domain"
}

nginx_domain_server_count() {
  domain=$1
  nginx_output=$(mktemp "${TMPDIR:-/var/tmp}/cpap-nginx-config.XXXXXX") \
    || die "无法创建 Nginx 配置检查文件"
  if ! nginx -T >"$nginx_output" 2>&1; then
    rm -f -- "$nginx_output"
    return 1
  fi
  count=$(awk -v domain="$domain" '
    {
      sub(/#.*/, "")
      if ($0 !~ /server_name[[:space:]]/) next
      sub(/.*server_name[[:space:]]+/, "")
      gsub(/;/, "")
      number = split($0, names, /[[:space:]]+/)
      for (position = 1; position <= number; position++) {
        if (names[position] == domain) count++
      }
    }
    END { print count + 0 }
  ' "$nginx_output")
  rm -f -- "$nginx_output"
  printf '%s\n' "$count"
}

inspect_existing_ingress() {
  domain=$1
  if command -v nginx >/dev/null 2>&1; then
    if systemctl is-active --quiet nginx >/dev/null 2>&1; then
      printf '%s\n' '检测：Nginx 已安装且正在运行' >&2
    else
      printf '%s\n' '检测：Nginx 已安装，但当前未运行' >&2
    fi
    if nginx_servers=$(nginx_domain_server_count "$domain"); then
      if [ "$nginx_servers" -gt 0 ]; then
        printf '%s\n' "检测：Nginx 已有 $nginx_servers 个 server_name 包含 $domain" >&2
      else
        printf '%s\n' "检测：Nginx 尚未声明 $domain" >&2
      fi
    else
      printf '%s\n' '检测：无法安全读取当前 Nginx 配置；不会覆盖任何站点' >&2
    fi
  else
    printf '%s\n' '检测：未安装 Nginx' >&2
  fi
  if [ -s "$CERTIFICATE_ROOT/$domain/fullchain.pem" ] \
    && [ -s "$CERTIFICATE_ROOT/$domain/privkey.pem" ]; then
    printf '%s\n' "检测：已存在 $domain 的 Let's Encrypt 证书" >&2
  else
    printf '%s\n' "检测：未找到 $domain 的 Let's Encrypt 证书" >&2
  fi
}

choose_ingress_mode() {
  domain=$1
  [ -t 0 ] || die "首次部署必须明确选择入口：sudo $SCRIPT_PATH run --ingress managed|external"
  printf '\n访问入口选择（仅首次选择，后续升级会复用）：\n' >&2
  inspect_existing_ingress "$domain"
  cat >&2 <<EOF

  1) 使用现有反向代理（不安装、不启动、不修改 Nginx 或 Certbot）
  2) 由 CPAP 管理 Nginx 与 Let's Encrypt 证书
  3) 取消
请选择 [1-3]:
EOF
  IFS= read -r ingress_choice
  case "$ingress_choice" in
    1) printf '%s\n' external ;;
    2) printf '%s\n' managed ;;
    *) die "已取消部署" ;;
  esac
}

resolve_deploy_ingress_mode() {
  config_file=$1
  explicit_mode=$2
  deploy_root=$3
  domain=$4
  stored_mode=
  stored_mode_explicit=false
  if [ -e "$config_file" ]; then
    stored_mode=$(config_ingress_mode "$config_file")
    if config_has_explicit_ingress_mode "$config_file"; then
      stored_mode_explicit=true
    fi
  fi
  if [ -n "$explicit_mode" ]; then
    explicit_mode=$(validate_ingress_mode "$explicit_mode") \
      || die "入口模式无效：${explicit_mode}（仅支持 managed 或 external）"
  fi
  if [ "$stored_mode_explicit" = true ]; then
    if [ -n "$explicit_mode" ] && [ "$stored_mode" != "$explicit_mode" ]; then
      die "已记录入口模式为 ${stored_mode}；请使用 sudo $SCRIPT_PATH ingress set $explicit_mode"
    fi
    printf '%s\n' "$stored_mode"
    return
  fi
  if [ -e "$deploy_root" ]; then
    if [ -n "$explicit_mode" ] && [ "$explicit_mode" != managed ]; then
      die "既有安装按兼容模式使用 managed；如需切换请使用 sudo $SCRIPT_PATH ingress set external"
    fi
    printf '%s\n' managed
    return
  fi
  if [ -n "$explicit_mode" ]; then
    printf '%s\n' "$explicit_mode"
  else
    choose_ingress_mode "$domain"
  fi
}

validate_version() {
  printf '%s' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$'
}

extract_github_release_tags() {
  LC_ALL=C awk '
    {
      remaining = $0
      while (match(remaining, /"tag_name"[[:space:]]*:[[:space:]]*"/)) {
        remaining = substr(remaining, RSTART + RLENGTH)
        closing_quote = index(remaining, "\"")
        if (closing_quote == 0) {
          break
        }
        print substr(remaining, 1, closing_quote - 1)
        remaining = substr(remaining, closing_quote + 1)
      }
    }
  '
}

github_releases_response_kind() {
  LC_ALL=C awk '
    { content = content $0 }
    END {
      gsub(/[[:space:]]/, "", content)
      if (content == "[]") {
        print "empty"
      } else if (substr(content, 1, 1) == "[" && substr(content, length(content), 1) == "]") {
        print "nonempty"
      } else {
        exit 1
      }
    }
  '
}

sort_newer_release_tags() {
  current_version=$1
  LC_ALL=C awk -v current="$current_version" '
    function valid(version) {
      return version ~ /^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$/
    }
    function numeric_value(value) {
      sub(/^0+/, "", value)
      return value == "" ? "0" : value
    }
    function numeric_compare(left, right, normalized_left, normalized_right) {
      normalized_left = numeric_value(left)
      normalized_right = numeric_value(right)
      if (length(normalized_left) != length(normalized_right)) {
        return length(normalized_left) < length(normalized_right) ? -1 : 1
      }
      if (normalized_left == normalized_right) {
        return 0
      }
      return normalized_left < normalized_right ? -1 : 1
    }
    function core_component(version, position, body, count, parts, patch_separator) {
      body = substr(version, 2)
      count = split(body, parts, ".")
      if (position == 1 || position == 2) {
        return parts[position]
      }
      patch_separator = index(parts[3], "-")
      return patch_separator == 0 ? parts[3] : substr(parts[3], 1, patch_separator - 1)
    }
    function prerelease(version, body, count, parts, patch_separator, value, index_value) {
      body = substr(version, 2)
      count = split(body, parts, ".")
      patch_separator = index(parts[3], "-")
      if (patch_separator > 0) {
        value = substr(parts[3], patch_separator + 1)
        for (index_value = 4; index_value <= count; index_value++) {
          value = value "." parts[index_value]
        }
        return value
      }
      if (count <= 3) {
        return ""
      }
      value = parts[4]
      for (index_value = 5; index_value <= count; index_value++) {
        value = value "." parts[index_value]
      }
      return value
    }
    function prerelease_compare(left, right, left_count, right_count, left_parts, right_parts, index_value, left_numeric, right_numeric, result) {
      if (left == right) {
        return 0
      }
      if (left == "") {
        return 1
      }
      if (right == "") {
        return -1
      }
      left_count = split(left, left_parts, ".")
      right_count = split(right, right_parts, ".")
      for (index_value = 1; index_value <= left_count && index_value <= right_count; index_value++) {
        if (left_parts[index_value] == right_parts[index_value]) {
          continue
        }
        left_numeric = left_parts[index_value] ~ /^[0-9]+$/
        right_numeric = right_parts[index_value] ~ /^[0-9]+$/
        if (left_numeric && right_numeric) {
          result = numeric_compare(left_parts[index_value], right_parts[index_value])
          if (result != 0) {
            return result
          }
          continue
        }
        if (left_numeric != right_numeric) {
          return left_numeric ? -1 : 1
        }
        return left_parts[index_value] < right_parts[index_value] ? -1 : 1
      }
      if (left_count == right_count) {
        return 0
      }
      return left_count < right_count ? -1 : 1
    }
    function version_compare(left, right, position, result) {
      for (position = 1; position <= 3; position++) {
        result = numeric_compare(core_component(left, position), core_component(right, position))
        if (result != 0) {
          return result
        }
      }
      return prerelease_compare(prerelease(left), prerelease(right))
    }
    BEGIN {
      if (!valid(current)) {
        exit 2
      }
    }
    {
      tag = $0
      if (valid(tag) && version_compare(tag, current) > 0 && !seen[tag]++) {
        tags[++tag_count] = tag
      }
    }
    END {
      for (left_index = 1; left_index <= tag_count; left_index++) {
        for (right_index = left_index + 1; right_index <= tag_count; right_index++) {
          comparison = version_compare(tags[left_index], tags[right_index])
          if (comparison < 0 || (comparison == 0 && tags[left_index] < tags[right_index])) {
            temporary = tags[left_index]
            tags[left_index] = tags[right_index]
            tags[right_index] = temporary
          }
        }
      }
      for (left_index = 1; left_index <= tag_count; left_index++) {
        print tags[left_index]
      }
    }
  '
}

query_github_release_tags() {
  repository=$1
  output_file=$2
  printf '%s' "$repository" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' \
    || die "GitHub 仓库标识无效：$repository"
  query_directory=$(mktemp -d "${TMPDIR:-/var/tmp}/cpap-releases.XXXXXX") \
    || die "创建 Release 查询目录失败"
  : >"$output_file"
  page=1
  while [ "$page" -le 60 ]; do
    response_file="$query_directory/releases-$page.json"
    page_tags="$query_directory/tags-$page.txt"
    if ! curl -fsSL --retry 3 --connect-timeout 15 --max-time 60 \
      -H 'Accept: application/vnd.github+json' \
      -H 'X-GitHub-Api-Version: 2022-11-28' \
      "https://api.github.com/repos/$repository/releases?per_page=100&page=$page" \
      -o "$response_file"; then
      rm -rf -- "$query_directory"
      die "查询 GitHub Releases 失败"
    fi
    if ! response_kind=$(github_releases_response_kind <"$response_file"); then
      rm -rf -- "$query_directory"
      die "GitHub Releases 返回了无效响应"
    fi
    extract_github_release_tags <"$response_file" >"$page_tags"
    release_count=$(awk 'END { print NR + 0 }' "$page_tags")
    if [ "$response_kind" = nonempty ] && [ "$release_count" -eq 0 ]; then
      rm -rf -- "$query_directory"
      die "GitHub Releases 响应不包含版本信息"
    fi
    [ "$release_count" -gt 0 ] || break
    awk '{ print }' "$page_tags" >>"$output_file"
    [ "$release_count" -eq 100 ] || break
    page=$((page + 1))
  done
  if [ "$page" -gt 60 ]; then
    rm -rf -- "$query_directory"
    die "GitHub Releases 数量超过安全查询上限"
  fi
  rm -rf -- "$query_directory"
}

choose_upgrade_release() {
  deploy_root=$1
  repository=$2
  current_version=$(deployment_marker_version "$deploy_root/.deploy-initialized" 2>/dev/null) \
    || die "未检测到有效的当前部署版本；请使用 --tag TAG 指定 Release，或不带 --tag 使用 Latest Release"
  release_directory=$(mktemp -d "${TMPDIR:-/var/tmp}/cpap-release-choice.XXXXXX") \
    || die "创建 Release 选择目录失败"
  all_tags="$release_directory/all-tags.txt"
  candidate_tags="$release_directory/candidates.txt"
  ui_step "检查可升级 Release"
  query_github_release_tags "$repository" "$all_tags"
  if ! sort_newer_release_tags "$current_version" <"$all_tags" >"$candidate_tags"; then
    rm -rf -- "$release_directory"
    die "比较 Release 版本失败"
  fi
  candidate_count=$(awk 'END { print NR + 0 }' "$candidate_tags")
  ui_note "当前部署  $current_version"
  if [ "$candidate_count" -eq 0 ]; then
    ui_done "没有比当前部署更新的 GitHub Release"
    rm -rf -- "$release_directory"
    return 2
  fi
  ui_done "发现 $candidate_count 个可升级 Release"
  printf '\n可升级版本：\n'
  candidate_number=0
  while IFS= read -r candidate_tag; do
    candidate_number=$((candidate_number + 1))
    if [ "$candidate_number" -eq 1 ]; then
      printf '  %d) %s  (最新)\n' "$candidate_number" "$candidate_tag"
    else
      printf '  %d) %s\n' "$candidate_number" "$candidate_tag"
    fi
  done <"$candidate_tags"
  if [ ! -t 0 ] || [ ! -t 1 ]; then
    ui_note "非交互模式仅展示版本，不执行升级；请重新运行 --tag TAG"
    rm -rf -- "$release_directory"
    return 2
  fi
  while :; do
    printf '\n请选择升级版本序号（q 取消）: ' >&2
    if ! IFS= read -r selected_number; then
      ui_note "未读取到选择，已取消升级"
      rm -rf -- "$release_directory"
      return 2
    fi
    case "$selected_number" in
      q|Q)
        ui_note "已取消升级"
        rm -rf -- "$release_directory"
        return 2
        ;;
      ''|*[!0-9]*) ui_error "请输入 1 到 $candidate_count 的序号，或输入 q 取消" ;;
      *)
        if [ "$selected_number" -ge 1 ] && [ "$selected_number" -le "$candidate_count" ]; then
          SELECTED_RELEASE_TAG=$(awk -v line="$selected_number" 'NR == line { print; exit }' "$candidate_tags")
          rm -rf -- "$release_directory"
          ui_done "已选择 $SELECTED_RELEASE_TAG"
          return 0
        fi
        ui_error "请输入 1 到 $candidate_count 的序号，或输入 q 取消"
        ;;
    esac
  done
}

release_value() {
  release_file=$1
  release_key=$2
  # Compatibility boundary: former release env fields remain readable.
  legacy_release_key="CPAC_${release_key#CPAP_}"
  value=$(awk -F= -v key="$release_key" -v legacy="$legacy_release_key" '
    $1 == key || $1 == legacy {
      if (seen[$1]++) invalid=1
      value=substr($0, index($0, "=") + 1)
      if (found++ && previous != value) invalid=1
      previous=value
    }
    END { if (!found || invalid) exit 1; print previous }
  ' "$release_file") || die "发布环境缺少唯一字段或新旧前缀冲突：$release_key"
  [ -n "$value" ] || die "发布环境字段为空：$release_key"
  printf '%s\n' "$value"
}

release_archive_name() {
  archive_name=$(release_value "$1" CPAP_RELEASE_ARCHIVE)
  case "$archive_name" in
    "codex-cpa-pool-$2.tar.gz") ;;
    # Compatibility boundary: published archives keep their immutable names.
    "codex-cpa-cluster-$2.tar.gz") ;;
    *) die "发布环境归档名不匹配" ;;
  esac
  printf '%s\n' "$archive_name"
}

validate_release_image() {
  printf '%s' "$1" | grep -Eq \
    '^[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._/-]+:sha256-[0-9a-f]{64}$'
}

install_prerequisites() {
  ingress_mode=$1
  validate_ingress_mode "$ingress_mode" >/dev/null \
    || die "入口模式无效：$ingress_mode"
  set --
  command -v curl >/dev/null 2>&1 || set -- "$@" curl ca-certificates
  command -v docker >/dev/null 2>&1 || set -- "$@" docker.io
  if [ "$ingress_mode" = managed ]; then
    command -v nginx >/dev/null 2>&1 || set -- "$@" nginx
    command -v certbot >/dev/null 2>&1 || set -- "$@" certbot
  fi
  command -v flock >/dev/null 2>&1 || set -- "$@" util-linux
  command -v sqlite3 >/dev/null 2>&1 || set -- "$@" sqlite3
  if [ "$#" -gt 0 ]; then
    command -v apt-get >/dev/null 2>&1 || die "缺少依赖且系统没有 apt-get：$*"
    apt-get update || die "更新系统软件索引失败"
    DEBIAN_FRONTEND=noninteractive apt-get install -y "$@" \
      || die "安装系统依赖失败：$*"
  fi
  for command in awk cmp cp curl docker flock getent grep install mktemp \
    readlink sed sha256sum sqlite3 systemctl tar wc; do
    require_command "$command"
  done
  if [ "$ingress_mode" = managed ]; then
    require_command nginx
    require_command certbot
  fi
  systemctl enable --now docker || die "启动 Docker 失败"
  if [ "$ingress_mode" = managed ]; then
    systemctl enable --now nginx || die "启动 Nginx 失败"
  fi
  if ! docker compose version >/dev/null 2>&1; then
    command -v apt-get >/dev/null 2>&1 || die "缺少 Docker Compose v2 且系统没有 apt-get"
    apt-get update || die "更新 Docker Compose 软件索引失败"
    DEBIAN_FRONTEND=noninteractive apt-get install -y docker-compose-v2 \
      || DEBIAN_FRONTEND=noninteractive apt-get install -y docker-compose-plugin \
      || die "无法安装 Docker Compose v2"
  fi
  docker info >/dev/null 2>&1 || die "Docker 服务不可用"
}

download_release() {
  repository=$1
  requested_tag=$2
  output_directory=$3
  if [ -n "$requested_tag" ]; then
    selected_version=$requested_tag
  else
    latest_url=$(curl -fsSL --retry 3 -o /dev/null -w '%{url_effective}' \
      "https://github.com/$repository/releases/latest") \
      || die "查询最新发布版本失败"
    selected_version=${latest_url##*/}
  fi
  validate_version "$selected_version" || die "无效 Release Tag：$selected_version"
  mkdir -p -- "$output_directory" || die "创建发布下载目录失败"
  base_url="https://github.com/$repository/releases/download/$selected_version"
  for asset in \
    "release-$selected_version.env" \
    run.sh \
    SHA256SUMS
  do
    curl -fsSL --retry 3 --connect-timeout 15 --max-time 300 \
      "$base_url/$asset" -o "$output_directory/$asset" \
      || die "下载发布文件失败：$asset"
  done
  archive_name=$(release_archive_name "$output_directory/release-$selected_version.env" "$selected_version")
  curl -fsSL --retry 3 --connect-timeout 15 --max-time 300 \
    "$base_url/$archive_name" -o "$output_directory/$archive_name" \
    || die "下载发布文件失败：$archive_name"
  printf '%s\n' "$selected_version" >"$output_directory/VERSION" \
    || die "记录发布版本失败"
}

verify_release() {
  directory=$1
  version=$2
  sums="$directory/SHA256SUMS"
  archive_name=$(release_archive_name "$directory/release-$version.env" "$version")
  for asset in "$archive_name" "release-$version.env" run.sh; do
    expected_sum=$(awk -v name="$asset" '
      $2 == name {
        found++
        hash=substr($0, 1, 64)
        if (length($0) != 66 + length(name) || substr($0, 65, 2) != "  " ||
            substr($0, 67) != name || hash !~ /^[0-9a-f]+$/) invalid=1
        value=hash
      }
      END { if (found != 1 || invalid) exit 1; print value }
    ' "$sums") || die "SHA256SUMS 缺少唯一精确条目：$asset"
    [ -f "$directory/$asset" ] && [ ! -L "$directory/$asset" ] \
      || die "发布文件缺失或不是普通文件：$asset"
    actual_sum=$(sha256sum "$directory/$asset")
    actual_sum=${actual_sum%% *}
    [ "$actual_sum" = "$expected_sum" ] || die "发布文件 SHA256 校验失败：$asset"
  done
  sh -n "$directory/run.sh" || die "发布中的 run.sh 语法无效"
}

extract_release() {
  archive=$1
  output_directory=$2
  entries=$(mktemp "${TMPDIR:-/var/tmp}/cpa-archive.XXXXXX") \
    || die "创建发布包检查文件失败"
  if ! tar -tzf "$archive" >"$entries"; then
    rm -f -- "$entries"
    die "读取发布包目录失败"
  fi
  while IFS= read -r entry; do
    normalized=${entry#./}
    case "$normalized" in
      ""|/*|../*|*/../*|*/..|*\\*)
        rm -f -- "$entries"
        die "发布包包含不安全路径：$entry"
        ;;
    esac
  done <"$entries"
  rm -f -- "$entries"
  mkdir -p -- "$output_directory" || die "创建发布解压目录失败"
  tar --no-same-owner --no-same-permissions -xzf "$archive" -C "$output_directory" \
    || die "解压发布包失败"
}

replace_release_file() {
  source_file=$1
  target_file=$2
  mode=$3
  target_directory=$(dirname -- "$target_file")
  [ -d "$target_directory" ] && [ ! -L "$target_directory" ] \
    || die "发布文件目录必须是普通目录：$target_directory"
  temporary_file=$(mktemp "$target_directory/.release-file.XXXXXX")
  if ! cp -- "$source_file" "$temporary_file" \
    || ! chmod "$mode" "$temporary_file" \
    || ! mv -f -- "$temporary_file" "$target_file"; then
    rm -f -- "$temporary_file"
    die "更新发布文件失败：$target_file"
  fi
}

install_release_metadata() {
  extracted_root=$1
  target_root=$2
  [ -f "$extracted_root/docker-compose.yml" ] && [ ! -L "$extracted_root/docker-compose.yml" ] \
    || die "发布包缺少 docker-compose.yml"
  [ -f "$extracted_root/release-manifest.json" ] && [ ! -L "$extracted_root/release-manifest.json" ] \
    || die "发布包缺少 release-manifest.json"
  replace_release_file "$extracted_root/docker-compose.yml" "$target_root/docker-compose.yml" 0644
  replace_release_file "$extracted_root/release-manifest.json" "$target_root/release-manifest.json" 0644
}

write_target_env() {
  release_file=$1
  expected_version=$2
  output=$3
  deploy_root=$4
  actual_version=$(release_value "$release_file" CPAP_RELEASE_VERSION)
  [ "$actual_version" = "$expected_version" ] || die "发布环境版本不匹配"
  release_archive_name "$release_file" "$expected_version" >/dev/null
  control_image=$(release_value "$release_file" CPAP_CONTROL_IMAGE)
  web_image=$(release_value "$release_file" CPAP_WEB_IMAGE)
  gateway_image=$(release_value "$release_file" CPAP_GATEWAY_IMAGE)
  edge_image=$(release_value "$release_file" CPAP_EDGE_IMAGE)
  for pair in \
    "control|$control_image" "web|$web_image" \
    "gateway|$gateway_image" "edge|$edge_image"
  do
    component=${pair%%|*}
    image=${pair#*|}
    validate_release_image "$image" || die "发布环境包含无效 $component 镜像"
  done
  output_directory=$(dirname -- "$output")
  temporary=$(mktemp "$output_directory/.target-env.XXXXXX")
  if [ -e "$output" ] || [ -L "$output" ]; then
    [ -f "$output" ] && [ ! -L "$output" ] \
      || die "既有 target.env 必须是普通非符号链接文件"
    # Existing deployment identity, network, ports, ownership and explicit
    # maintenance choices are operator state; a product rename never resets them.
    if ! awk -v control="$control_image" -v web="$web_image" \
      -v gateway="$gateway_image" -v edge="$edge_image" '
      BEGIN {
        images["CPA_CONTROL_IMAGE"]=control; images["CPA_WEB_IMAGE"]=web
        images["CPA_GATEWAY_IMAGE"]=gateway; images["CPA_EDGE_IMAGE"]=edge
      }
      {
        key=$0; sub(/=.*/, "", key)
        if (key in images) {
          if (seen[key]++) invalid=1
          print key "=" images[key]
        } else print
      }
      END {
        for (key in images) if (!seen[key]) invalid=1
        if (invalid) exit 1
      }
    ' "$output" >"$temporary" \
      || ! chmod 0600 "$temporary" || ! mv -f -- "$temporary" "$output"; then
      rm -f -- "$temporary"
      die "更新既有目标镜像失败；target.env 必须包含唯一的四个镜像字段"
    fi
    return 0
  fi
  if ! {
    printf 'CPA_CONTROL_IMAGE=%s\n' "$control_image"
    printf 'CPA_WEB_IMAGE=%s\n' "$web_image"
    printf 'CPA_GATEWAY_IMAGE=%s\n' "$gateway_image"
    printf 'CPA_EDGE_IMAGE=%s\n' "$edge_image"
    printf '%s\n' \
      'CPA_COMPOSE_PROJECT_NAME=codex-cpa' \
      'CPA_INSTANCE_NAME=codex-cpa' \
      'CPA_RUNTIME_OWNER=codex-cpa' \
      'CPA_OWNERSHIP_ACTIVATION_TTL=2m'
    printf 'CPA_DEPLOY_ROOT=%s\nCPA_CONFIRM_DEPLOY_ROOT=%s\n' "$deploy_root" "$deploy_root"
    printf '%s\n' \
      'CPA_PUBLIC_BIND_ADDRESS=127.0.0.1' \
      'CPA_PUBLIC_PROBE_HOST=127.0.0.1' \
      'CPA_PUBLIC_PORT=18317' \
      'CPA_INTERNAL_PORT=18316' \
      'CPA_ALLOW_EDGE_RECREATE=true'
    printf 'CPA_CONFIRM_EDGE_MAINTENANCE=%s\n' "$deploy_root"
    printf '%s\n' \
      'CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS=1' \
      'CPA_GATEWAY_FORCE_STOP_ON_DRAIN_TIMEOUT=true' \
      'CPA_DOCKER_SOCKET_PATH=/var/run/docker.sock' \
      'CPA_ACCOUNT_COMPOSE_PROJECT=cliproxy-multi' \
      'CPA_ACCOUNT_INSTANCE_NAME=cliproxy' \
      'CPA_UPSTREAM_NETWORK=cliproxy-backend' \
      'CPA_BOOTSTRAP_MODE=controlled-cutover'
    printf 'CPA_CONFIRM_WRITERS_STOPPED=%s\n' "$deploy_root"
  } >"$temporary" || ! chmod 0600 "$temporary" || ! mv -f -- "$temporary" "$output"; then
    rm -f -- "$temporary"
    die "写入目标环境失败：$output"
  fi
}

target_initialized() {
  root=$1
  for authoritative_file in \
    "$root/state/control-plane.sqlite3" \
    "$root/state/usage.sqlite3" \
    "$root/secrets/control-plane.key"
  do
    [ -f "$authoritative_file" ] && [ ! -L "$authoritative_file" ] || return 1
  done
}

validate_upgrade_target() {
  root=$1
  canonical_root=$(CDPATH= cd -- "$root" && pwd -P) \
    || die "无法解析部署根目录：$root"
  [ "$canonical_root" = "$root" ] \
    || die "部署根目录必须是规范路径且不能经过符号链接：$root"
  for runtime_directory in \
    state state/gateway state/edge secrets logs logs/gateway
  do
    [ -d "$root/$runtime_directory" ] && [ ! -L "$root/$runtime_directory" ] \
      || die "升级前置目录缺失或不是普通目录：$root/$runtime_directory"
  done
  for runtime_file in \
    state/control-plane.sqlite3 \
    state/usage.sqlite3 \
    secrets/control-plane.key \
    state/edge/active-gateway.conf \
    target.env \
    docker-compose.yml \
    release-manifest.json
  do
    [ -f "$root/$runtime_file" ] && [ ! -L "$root/$runtime_file" ] \
      || die "升级前置文件缺失或不是普通文件：$root/$runtime_file"
  done
}

validate_optional_account_runtime_layout() {
  root=$1
  for relative in management management/config management/config/static; do
    path="$root/$relative"
    if [ -L "$path" ] || { [ -e "$path" ] && [ ! -d "$path" ]; }; then
      die "账号运行目录必须是普通目录且不能是符号链接：$path"
    fi
    if [ -d "$path" ]; then
      canonical_path=$(CDPATH= cd -- "$path" && pwd -P) \
        || die "无法解析账号运行目录：$path"
      [ "$canonical_path" = "$path" ] \
        || die "账号运行目录不能经过符号链接：$path"
    fi
  done
}

ensure_account_runtime_layout() {
  root=$1
  validate_optional_account_runtime_layout "$root"
  for relative in management management/config management/config/static; do
    path="$root/$relative"
    if [ ! -d "$path" ]; then
      mkdir -- "$path" || die "创建账号运行目录失败：$path"
    fi
    chmod 0700 "$path" || die "设置账号运行目录权限失败：$path"
  done
  validate_optional_account_runtime_layout "$root"
}

backup_target() {
  root=$1
  backup_directory=${CPAP_BACKUP_DIR:-$DEFAULT_BACKUP_ROOT}
  mkdir -p -- "$backup_directory"
  chmod 0700 "$backup_directory"
  timestamp=$(date -u +%Y%m%dT%H%M%SZ)
  backup="$backup_directory/codex-cpa-$timestamp.tar.gz"
  # Keep recovery inputs only; logs, generated snapshots, live database files
  # and historical backups must never be copied into the staging directory.
  set --
  for relative in secrets auth configs management target.env docker-compose.yml \
    compose.accounts.yml release-manifest.json .deploy-initialized \
    state/compose.env state/edge/active-gateway.conf; do
    [ -e "$root/$relative" ] && set -- "$@" "$relative"
  done
  [ "$#" -gt 0 ] || die "没有可备份的 CPA 目标内容：$root"
  backup_staging=$(mktemp -d "$backup_directory/.backup-staging.XXXXXX")
  backup_temporary=$(mktemp "$backup_directory/.backup-archive.XXXXXX")
  if ! chmod 0700 "$backup_staging" \
    || ! rm -f -- "$backup_temporary"; then
    rm -rf -- "$backup_staging"
    rm -f -- "$backup_temporary"
    die "准备升级备份目录失败：$backup_directory"
  fi
  mkdir -p -- "$backup_staging/state/edge"
  for relative in "$@"; do
    if ! cp -a -- "$root/$relative" "$backup_staging/$relative"; then
      rm -rf -- "$backup_staging"
      rm -f -- "$backup_temporary"
      die "复制升级备份内容失败：$root/$relative"
    fi
  done
  for database in control-plane.sqlite3 usage.sqlite3; do
    source_database="$root/state/$database"
    snapshot_database="$backup_staging/state/$database"
    rm -f -- "$snapshot_database" "$snapshot_database-wal" "$snapshot_database-shm"
    escaped_snapshot=$(printf '%s' "$snapshot_database" | sed "s/'/''/g")
    # Pin a WAL read snapshot before the Backup API starts. Without this read
    # transaction, concurrent collector commits repeatedly restart the copy.
    printf '  →  备份数据库 %s\n' "$database" >&2
    if ! sqlite3 -readonly -bail "$source_database" <<EOF
.timeout 5000
BEGIN;
.output /dev/null
SELECT count(*) FROM sqlite_schema;
.output stdout
.backup '$escaped_snapshot'
ROLLBACK;
EOF
    then
      rm -rf -- "$backup_staging"
      rm -f -- "$backup_temporary"
      die "创建 SQLite 一致性副本失败：$source_database"
    fi
    snapshot_check=$(sqlite3 "$snapshot_database" 'PRAGMA quick_check;') || {
      rm -rf -- "$backup_staging"
      rm -f -- "$backup_temporary"
      die "检查 SQLite 备份失败：$database"
    }
    [ "$snapshot_check" = ok ] || {
      rm -rf -- "$backup_staging"
      rm -f -- "$backup_temporary"
      die "SQLite 备份完整性失败：$database"
    }
    chmod 0600 "$snapshot_database"
    set -- "$@" "state/$database"
  done
  if ! tar -C "$backup_staging" -czf "$backup_temporary" "$@" \
    || ! chmod 0600 "$backup_temporary" \
    || ! mv -f -- "$backup_temporary" "$backup"; then
    rm -rf -- "$backup_staging"
    rm -f -- "$backup_temporary"
    die "写入升级备份归档失败：$backup"
  fi
  rm -rf -- "$backup_staging"
  backup_staging=
  backup_temporary=
  printf '%s\n' "$backup"
}

claim_admin_key() {
  config_file=$1
  pending="$(dirname -- "$config_file")/bootstrap-admin.key"
  [ -f "$pending" ] && [ ! -L "$pending" ] || die "没有待领取的首次管理员凭据"
  [ -t 1 ] || die "管理员凭据只能在交互终端领取：sudo $SCRIPT_PATH admin-key claim"
  printf '\n%s%s首次管理员管理密钥%s  %s仅显示一次，请立即保存%s\n' \
    "$UI_BOLD" "$UI_YELLOW" "$UI_RESET" "$UI_DIM" "$UI_RESET"
  printf '%s\n' "$(cat "$pending")"
  rm -f -- "$pending"
}

write_nginx_site() {
  mode=$1
  domain=$2
  available_directory=$NGINX_AVAILABLE_DIRECTORY
  enabled_directory=$NGINX_ENABLED_DIRECTORY
  site_file="$available_directory/$domain.conf"
  enabled_file="$enabled_directory/$domain.conf"
  mkdir -p -- "$available_directory" "$enabled_directory" "$ACME_ROOT"
  if [ -e "$site_file" ] || [ -L "$site_file" ]; then
    [ -f "$site_file" ] && [ ! -L "$site_file" ] \
      || die "Nginx 站点必须是普通非符号链接文件：$site_file"
    if grep -Fxq '# Managed by Codex CPA Pool run.sh' "$site_file"; then
      :
    elif grep -Fxq '# Managed by CPAC run.sh' "$site_file" \
      || grep -Fxq '# Managed by CPAC deploy.sh' "$site_file"; then
      ui_note "检测到旧版 Nginx 托管标记，本次将迁移为 Codex CPA Pool run.sh"
    else
      die "Nginx 已有未托管的同名站点，拒绝覆盖：${site_file}；请改用 external 或手动处理"
    fi
    site_backup=$(mktemp "$available_directory/.cpa-site-backup.XXXXXX")
    cp -p -- "$site_file" "$site_backup"
    had_site=true
  else
    site_backup=
    had_site=false
  fi
  if nginx_domain_servers=$(nginx_domain_server_count "$domain"); then
    if [ "$had_site" = false ] && [ "$nginx_domain_servers" -gt 0 ]; then
      die "Nginx 已存在 $domain 的未托管站点，拒绝新增冲突配置；请改用 external 或手动处理"
    fi
    if [ "$nginx_domain_servers" -gt 2 ]; then
      die "Nginx 检测到多个 $domain 站点，拒绝覆盖；请改用 external 或手动处理"
    fi
  else
    die "无法安全读取当前 Nginx 配置，拒绝修改站点：$domain"
  fi
  if [ -e "$enabled_file" ] || [ -L "$enabled_file" ]; then
    [ -L "$enabled_file" ] && [ "$(readlink -f -- "$enabled_file")" = "$site_file" ] \
      || { [ -z "$site_backup" ] || rm -f -- "$site_backup"; die "Nginx 启用项不是预期符号链接：$enabled_file"; }
    had_enabled=true
  else
    had_enabled=false
  fi
  nginx_tmp=$(mktemp "$available_directory/.cpa-site.XXXXXX")
  if [ "$mode" = http ]; then
    cat >"$nginx_tmp" <<EOF
# Managed by Codex CPA Pool run.sh
server {
    listen 80;
    server_name $domain;

    location ^~ /.well-known/acme-challenge/ {
        root $ACME_ROOT;
        try_files \$uri =404;
    }

    location / {
        return 503;
    }
}
EOF
  else
    cat >"$nginx_tmp" <<EOF
# Managed by Codex CPA Pool run.sh
server {
    listen 80;
    server_name $domain;

    location ^~ /.well-known/acme-challenge/ {
        root $ACME_ROOT;
        try_files \$uri =404;
    }

    location / {
        return 301 https://\$host\$request_uri;
    }
}

server {
    listen 443 ssl http2;
    server_name $domain;

    ssl_certificate $CERTIFICATE_ROOT/$domain/fullchain.pem;
    ssl_certificate_key $CERTIFICATE_ROOT/$domain/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;

    client_max_body_size 100m;

    location / {
        proxy_pass http://127.0.0.1:${OPERATOR_PUBLIC_PORT};
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Connection "";

        proxy_connect_timeout 30s;
        proxy_send_timeout 3600s;
        proxy_read_timeout 3600s;

        proxy_buffering off;
        proxy_request_buffering off;
        proxy_cache off;
        add_header X-Accel-Buffering no always;
    }
}
EOF
  fi
  if ! chmod 0644 "$nginx_tmp" || ! mv -f -- "$nginx_tmp" "$site_file"; then
    rm -f -- "$nginx_tmp"
    [ -z "$site_backup" ] || rm -f -- "$site_backup"
    die "写入 Nginx 站点失败"
  fi
  ln -sfn "$site_file" "$enabled_file"
  nginx_log=$(mktemp "${TMPDIR:-/var/tmp}/cpap-nginx.XXXXXX")
  if ! nginx -t >"$nginx_log" 2>&1 \
    || ! systemctl reload nginx >>"$nginx_log" 2>&1; then
    cat "$nginx_log" >&2
    if [ "$had_site" = true ]; then
      mv -f -- "$site_backup" "$site_file"
      site_backup=
    else
      rm -f -- "$site_file"
    fi
    [ "$had_enabled" = true ] || rm -f -- "$enabled_file"
    nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
    rm -f -- "$nginx_log"
    die "Nginx 配置验证或重载失败，已恢复原站点"
  fi
  rm -f -- "$nginx_log"
  [ -z "$site_backup" ] || rm -f -- "$site_backup"
}

resolve_certbot_email() {
  certbot_email=${CPAP_CERTBOT_EMAIL:-}
  if [ -z "$certbot_email" ]; then
    [ -t 0 ] || die "首次签发证书需要交互输入邮箱，或设置 CPAP_CERTBOT_EMAIL"
    printf "%s" "请输入 Let's Encrypt 通知邮箱: " >&2
    IFS= read -r certbot_email
  fi
  printf '%s\n' "$certbot_email" \
    | awk 'length($0) <= 254 && $0 ~ /^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$/ { valid=1 } END { exit valid ? 0 : 1 }' \
    || die "证书通知邮箱格式无效"
}

configure_nginx_tls() {
  domain=$1
  certificate_directory="$CERTIFICATE_ROOT/$domain"
  if [ ! -s "$certificate_directory/fullchain.pem" ] || [ ! -s "$certificate_directory/privkey.pem" ]; then
    write_nginx_site http "$domain"
    resolve_certbot_email
    certbot certonly --webroot --webroot-path "$ACME_ROOT" \
      --domain "$domain" --email "$certbot_email" --agree-tos --non-interactive --quiet
  fi
  [ -s "$certificate_directory/fullchain.pem" ] && [ -s "$certificate_directory/privkey.pem" ] \
    || die "TLS 证书不存在：$domain"
  write_nginx_site https "$domain"
}

show_external_ingress_contract() {
  domain=$1
  ui_step "复用现有反向代理"
  ui_done "未安装、启动或修改 Nginx / Certbot"
  cat <<EOF

CPAP 本机上游：http://127.0.0.1:${OPERATOR_PUBLIC_PORT}
现有反向代理必须：
  - 保留 Host、X-Forwarded-For、X-Forwarded-Proto；
  - 透传 WebSocket 的 Upgrade / Connection；SSE 必须关闭响应缓冲；
  - 为流式响应设置至少 3600 秒的读取和发送超时；
  - 将 ${domain}/__health 转发到该上游，公网预期返回 HTTP 200。

Nginx 示例（按你的站点规范合并，不由 CPAP 写入）：
  location / {
      proxy_pass http://127.0.0.1:${OPERATOR_PUBLIC_PORT};
      proxy_http_version 1.1;
      proxy_set_header Host \$host;
      proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto \$scheme;
      proxy_set_header Upgrade \$http_upgrade;
      proxy_set_header Connection \$http_connection;
      proxy_buffering off;
      proxy_request_buffering off;
      proxy_read_timeout 3600s;
      proxy_send_timeout 3600s;
  }
EOF
}

update_operator_script() {
  verified_script=$1
  [ -f "$SCRIPT_PATH" ] && [ ! -L "$SCRIPT_PATH" ] \
    || die "当前部署入口必须是普通非符号链接文件：$SCRIPT_PATH"
  # Compatibility boundary: a pinned pre-rename release must not downgrade
  # the installer and lose the resolved CPAP paths or upgrade safeguards.
  if ! grep -Fxq '# CPAP_OPERATOR_PROTOCOL=1' "$verified_script" \
    && grep -q 'CPAC_' "$verified_script"; then
    ui_note "所选历史发布使用旧安装器；保留当前 Pool 安装器以原址升级"
    return 1
  fi
  if ! grep -Fxq '# CPAP_RUNTIME_ROOT_ALIAS=1' "$verified_script"; then
    ui_note "所选发布的安装器不支持运行目录入口解析；保留当前安装器以原址升级"
    return 1
  fi
  if ! grep -Fxq '# CPAP_RUNTIME_ROOT_CONFIG=1' "$verified_script"; then
    ui_note "所选发布的安装器不支持已记录的运行目录；保留当前安装器"
    return 1
  fi
  if ! grep -Fxq '# CPAP_SNAPSHOT_BACKUP=1' "$verified_script"; then
    ui_note "所选发布的安装器缺少在线快照备份修复；保留当前安装器"
    return 1
  fi
  if cmp -s "$verified_script" "$SCRIPT_PATH"; then
    return 1
  fi
  script_tmp=$(mktemp "$SCRIPT_DIRECTORY/.run.XXXXXX")
  if ! install -m 0755 "$verified_script" "$script_tmp" \
    || ! mv -f -- "$script_tmp" "$SCRIPT_PATH"; then
    rm -f -- "$script_tmp"
    die "更新部署入口失败：$SCRIPT_PATH"
  fi
  return 0
}

remove_legacy_operator_script() {
  legacy_script="$STAGING_ROOT/deploy.sh"
  [ "$legacy_script" != "$SCRIPT_PATH" ] || return 0
  [ -e "$legacy_script" ] || [ -L "$legacy_script" ] || return 0
  if [ ! -f "$legacy_script" ] || [ -L "$legacy_script" ]; then
    ui_note "旧 deploy.sh 不是普通文件，已保留并等待人工检查"
    return 0
  fi
  if grep -Fq 'DEFAULT_REPOSITORY=${CPAC_GITHUB_REPOSITORY:-Alfonsxh/codex-cpa-cluster}' "$legacy_script" \
    && grep -Fq 'ENTRY_COMMAND=${1:-deploy}' "$legacy_script"; then
    if rm -f -- "$legacy_script"; then
      ui_note "已移除旧版 deploy.sh，仅保留 run.sh"
    else
      ui_note "无法移除旧版 deploy.sh，请完成后人工检查"
    fi
  else
    ui_note "旧 deploy.sh 无法确认来源，已保留并等待人工检查"
  fi
}

run_target_action() {
  deploy_root=$1
  action=$2
  CPA_RELEASE_ROOT="$deploy_root" CPA_ENV_FILE="$deploy_root/target.env" \
    sh "$SCRIPT_PATH" __target "$action"
}

target_action_label() {
  case "$1" in
    config) printf '%s\n' "检查部署配置" ;;
    pull) printf '%s\n' "拉取 Control / Web / Gateway / Edge 镜像" ;;
    verify-images) printf '%s\n' "验证镜像身份" ;;
    activate) printf '%s\n' "激活运行时所有权" ;;
    up-core) printf '%s\n' "蓝绿更新 Gateway 与核心服务" ;;
    up-writers) printf '%s\n' "更新后台任务容器" ;;
    smoke) printf '%s\n' "执行本机健康检查" ;;
    *) printf '%s\n' "$1" ;;
  esac
}

deployment_env_value() {
  env_file=$1
  env_key=$2
  [ -f "$env_file" ] && [ ! -L "$env_file" ] || return 1
  awk -F= -v key="$env_key" \
    '$1 == key { print substr($0, index($0, "=") + 1); found++ } END { if (found != 1) exit 1 }' \
    "$env_file"
}

deployment_instance_name() {
  instance_env=$1
  [ -f "$instance_env" ] && [ ! -L "$instance_env" ] \
    || die "target.env 必须是普通非符号链接文件"
  instance_value=$(awk -F= '
    $1 == "CPA_INSTANCE_NAME" { value=substr($0, index($0, "=") + 1); found++ }
    END {
      if (found > 1) exit 1
      if (!found) print "codex-cpa"; else print value
    }
  ' "$instance_env") || die "target.env 的 CPA_INSTANCE_NAME 不能重复"
  printf '%s' "$instance_value" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9_.-]*$' \
    || die "CPA_INSTANCE_NAME 无效"
  printf '%s\n' "$instance_value"
}

deployment_upstream_network() {
  network_value=$(deployment_env_value "$1" CPA_UPSTREAM_NETWORK) \
    || die "target.env 必须包含唯一 CPA_UPSTREAM_NETWORK"
  printf '%s' "$network_value" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9_.-]*$' \
    || die "CPA_UPSTREAM_NETWORK 无效"
  printf '%s\n' "$network_value"
}

operator_public_port() {
  port_root=$1
  if [ ! -e "$port_root" ] && [ ! -L "$port_root" ]; then
    printf '%s\n' 18317
    return 0
  fi
  port_value=$(deployment_env_value "$port_root/target.env" CPA_PUBLIC_PORT) \
    || die "既有 target.env 必须包含唯一 CPA_PUBLIC_PORT"
  case "$port_value" in
    ''|*[!0-9]*) die "CPA_PUBLIC_PORT 必须是 1 到 65535 的端口" ;;
  esac
  [ "${#port_value}" -le 5 ] && [ "$port_value" -ge 1 ] && [ "$port_value" -le 65535 ] \
    || die "CPA_PUBLIC_PORT 必须是 1 到 65535 的端口"
  printf '%s\n' "$port_value"
}

deployment_marker_version() {
  marker_file=$1
  [ -f "$marker_file" ] && [ ! -L "$marker_file" ] || return 1
  marker_version=$(awk -F= \
    '$1 == "version" { print substr($0, index($0, "=") + 1); found++ } END { if (found != 1) exit 1 }' \
    "$marker_file") || return 1
  validate_version "$marker_version" || return 1
  printf '%s\n' "$marker_version"
}

deployment_image_digest() {
  image_reference=$1
  image_digest=${image_reference##*:sha256-}
  if [ "$image_digest" = "$image_reference" ] || [ -z "$image_digest" ]; then
    printf '%s\n' 未知
  else
    printf '%.12s\n' "$image_digest"
  fi
}

deployment_image_change() {
  previous_image=$1
  selected_image=$2
  selected_digest=$(deployment_image_digest "$selected_image")
  if [ -z "$previous_image" ]; then
    printf '安装 %s\n' "$selected_digest"
  elif [ "$previous_image" = "$selected_image" ]; then
    printf '复用 %s\n' "$selected_digest"
  else
    previous_digest=$(deployment_image_digest "$previous_image")
    printf '更新 %s -> %s\n' "$previous_digest" "$selected_digest"
  fi
}

deployment_active_slot() {
  selection_file=$1
  [ -f "$selection_file" ] && [ ! -L "$selection_file" ] || {
    printf '%s\n' 未知
    return
  }
  selection=$(cat "$selection_file" 2>/dev/null || true)
  case "$selection" in
    'set $active_gateway_backend gateway-blue:8317;') printf '%s\n' blue ;;
    'set $active_gateway_backend gateway-green:8317;') printf '%s\n' green ;;
    *) printf '%s\n' 未知 ;;
  esac
}

deployment_container_id() {
  container_name=$1
  container_id=$(docker inspect --format '{{.Id}}' "$container_name" 2>/dev/null || true)
  if [ -n "$container_id" ] && [ "$container_id" != '{}' ]; then
    printf '%s\n' "$container_id"
  else
    printf '%s\n' 未运行
  fi
}

deployment_container_change() {
  previous_id=$1
  current_id=$2
  if [ "$previous_id" = 未运行 ] && [ "$current_id" != 未运行 ]; then
    printf '%s\n' 新建
  elif [ "$previous_id" = "$current_id" ] && [ "$current_id" != 未运行 ]; then
    printf '%s\n' 复用
  elif [ "$current_id" = 未运行 ]; then
    printf '%s\n' 未运行
  else
    printf '%s\n' 更新
  fi
}

prepare_deployment_summary() {
  summary_root=$1
  summary_release_file=$2
  summary_fresh=$3
  DEPLOY_SUMMARY_PREVIOUS_VERSION=未安装
  DEPLOY_SUMMARY_INSTANCE=codex-cpa
  previous_control_image=
  previous_web_image=
  previous_gateway_image=
  previous_edge_image=
  if [ "$summary_fresh" = false ]; then
    DEPLOY_SUMMARY_INSTANCE=$(deployment_instance_name "$summary_root/target.env")
    DEPLOY_SUMMARY_PREVIOUS_VERSION=$(deployment_marker_version \
      "$summary_root/.deploy-initialized" 2>/dev/null || printf '%s\n' 未知)
    previous_control_image=$(deployment_env_value "$summary_root/target.env" CPA_CONTROL_IMAGE 2>/dev/null || true)
    previous_web_image=$(deployment_env_value "$summary_root/target.env" CPA_WEB_IMAGE 2>/dev/null || true)
    previous_gateway_image=$(deployment_env_value "$summary_root/target.env" CPA_GATEWAY_IMAGE 2>/dev/null || true)
    previous_edge_image=$(deployment_env_value "$summary_root/target.env" CPA_EDGE_IMAGE 2>/dev/null || true)
  fi
  selected_control_image=$(release_value "$summary_release_file" CPAP_CONTROL_IMAGE)
  selected_web_image=$(release_value "$summary_release_file" CPAP_WEB_IMAGE)
  selected_gateway_image=$(release_value "$summary_release_file" CPAP_GATEWAY_IMAGE)
  selected_edge_image=$(release_value "$summary_release_file" CPAP_EDGE_IMAGE)
  DEPLOY_SUMMARY_CONTROL_IMAGE=$(deployment_image_change "$previous_control_image" "$selected_control_image")
  DEPLOY_SUMMARY_WEB_IMAGE=$(deployment_image_change "$previous_web_image" "$selected_web_image")
  DEPLOY_SUMMARY_GATEWAY_IMAGE=$(deployment_image_change "$previous_gateway_image" "$selected_gateway_image")
  DEPLOY_SUMMARY_EDGE_IMAGE=$(deployment_image_change "$previous_edge_image" "$selected_edge_image")

  DEPLOY_SUMMARY_BEFORE_SLOT=$(deployment_active_slot "$summary_root/state/edge/active-gateway.conf")
  DEPLOY_SUMMARY_BEFORE_GATEWAY_BLUE=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-gateway-blue")
  DEPLOY_SUMMARY_BEFORE_GATEWAY_GREEN=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-gateway-green")
  DEPLOY_SUMMARY_BEFORE_ADMIN=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-admin")
  DEPLOY_SUMMARY_BEFORE_WEB=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-web")
  DEPLOY_SUMMARY_BEFORE_EDGE=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-edge")
  DEPLOY_SUMMARY_BEFORE_QUOTA=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-quota")
  DEPLOY_SUMMARY_BEFORE_COLLECTOR=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-usage-collector")
  DEPLOY_SUMMARY_BEFORE_FAILOVER=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-account-failover")
  DEPLOY_SUMMARY_BEFORE_LOGS=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-log-maintenance")
  DEPLOY_SUMMARY_BEFORE_NOTIFICATIONS=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-notifications")
}

collect_core_deployment_summary() {
  summary_root=$1
  current_slot=$(deployment_active_slot "$summary_root/state/edge/active-gateway.conf")
  current_gateway_blue=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-gateway-blue")
  current_gateway_green=$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-gateway-green")
  blue_action=$(deployment_container_change "$DEPLOY_SUMMARY_BEFORE_GATEWAY_BLUE" "$current_gateway_blue")
  green_action=$(deployment_container_change "$DEPLOY_SUMMARY_BEFORE_GATEWAY_GREEN" "$current_gateway_green")
  if [ "$DEPLOY_SUMMARY_BEFORE_SLOT" = 未知 ]; then
    DEPLOY_SUMMARY_GATEWAY_ACTION="初始化 blue/green；活动槽 $current_slot"
  elif [ "$DEPLOY_SUMMARY_BEFORE_SLOT" != "$current_slot" ]; then
    DEPLOY_SUMMARY_GATEWAY_ACTION="$DEPLOY_SUMMARY_BEFORE_SLOT -> ${current_slot}；原槽更新完成；双槽已对齐"
  elif [ "$blue_action" = 复用 ] && [ "$green_action" = 复用 ]; then
    DEPLOY_SUMMARY_GATEWAY_ACTION="保持 ${current_slot}；双槽复用，无需切换"
  else
    DEPLOY_SUMMARY_GATEWAY_ACTION="保持 ${current_slot}；blue ${blue_action}；green $green_action"
  fi
  admin_action=$(deployment_container_change "$DEPLOY_SUMMARY_BEFORE_ADMIN" \
    "$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-admin")")
  web_action=$(deployment_container_change "$DEPLOY_SUMMARY_BEFORE_WEB" \
    "$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-web")")
  edge_action=$(deployment_container_change "$DEPLOY_SUMMARY_BEFORE_EDGE" \
    "$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-edge")")
  DEPLOY_SUMMARY_CORE_ACTIONS="Admin ${admin_action}；Web ${web_action}；Edge $edge_action"
}

collect_writer_deployment_summary() {
  quota_action=$(deployment_container_change "$DEPLOY_SUMMARY_BEFORE_QUOTA" \
    "$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-quota")")
  collector_action=$(deployment_container_change "$DEPLOY_SUMMARY_BEFORE_COLLECTOR" \
    "$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-usage-collector")")
  failover_action=$(deployment_container_change "$DEPLOY_SUMMARY_BEFORE_FAILOVER" \
    "$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-account-failover")")
  logs_action=$(deployment_container_change "$DEPLOY_SUMMARY_BEFORE_LOGS" \
    "$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-log-maintenance")")
  notifications_action=$(deployment_container_change "$DEPLOY_SUMMARY_BEFORE_NOTIFICATIONS" \
    "$(deployment_container_id "$DEPLOY_SUMMARY_INSTANCE-notifications")")
  DEPLOY_SUMMARY_WRITER_ACTIONS="quota ${quota_action}；collector ${collector_action}；failover ${failover_action}；logs ${logs_action}；notifications $notifications_action"
}

show_target_action_details() {
  summary_action=$1
  summary_root=$2
  case "$summary_action" in
    config)
      ui_note "Compose、发布清单与 target.env 配置一致"
      ;;
    pull)
      ui_note "Control  $DEPLOY_SUMMARY_CONTROL_IMAGE"
      ui_note "Web      $DEPLOY_SUMMARY_WEB_IMAGE"
      ui_note "Gateway  $DEPLOY_SUMMARY_GATEWAY_IMAGE"
      ui_note "Edge     $DEPLOY_SUMMARY_EDGE_IMAGE"
      ;;
    verify-images)
      ui_note "4/4 镜像标签、组件指纹与发布清单一致"
      ;;
    activate)
      ui_note "运行时所有权已确认：codex-cpa"
      ;;
    up-core)
      collect_core_deployment_summary "$summary_root"
      ui_note "Gateway  $DEPLOY_SUMMARY_GATEWAY_ACTION"
      ui_note "$DEPLOY_SUMMARY_CORE_ACTIONS"
      ;;
    up-writers)
      collect_writer_deployment_summary
      ui_note "$DEPLOY_SUMMARY_WRITER_ACTIONS"
      ;;
    smoke)
      ui_note "健康、鉴权边界、内部探针与 Web 路由检查通过"
      ;;
  esac
}

write_initialized_marker() {
  deploy_root=$1
  selected_version=$2
  marker_tmp=$(mktemp "$deploy_root/.deploy-initialized.XXXXXX")
  if ! printf 'version=%s\n' "$selected_version" >"$marker_tmp" \
    || ! chmod 0600 "$marker_tmp" \
    || ! mv -f -- "$marker_tmp" "$deploy_root/.deploy-initialized"; then
    rm -f -- "$marker_tmp"
    die "写入部署版本标记失败"
  fi
  rm -f -- "$deploy_root/.cpac-initialized"
}

prune_legacy_release_payload() {
  deploy_root=$1
  for relative in \
    .dockerignore .env.example Dockerfile Makefile CHANGELOG.md CODE_OF_CONDUCT.md \
    CONTRIBUTING.md LICENSE README.md SECURITY.md api cmd docker-compose.test.yml docs frontend \
    go.mod go.sum internal scripts testdata tools
  do
    legacy_path="$deploy_root/$relative"
    [ ! -e "$legacy_path" ] && [ ! -L "$legacy_path" ] || rm -rf -- "$legacy_path"
  done
}

run_install_or_upgrade() {
  config_file=$DEFAULT_CONFIG_FILE
  repository=$DEFAULT_REPOSITORY
  explicit_domain=${CPAP_DOMAIN:-}
  explicit_ingress_mode=${CPAP_INGRESS_MODE:-}
  version=
  tag_menu_requested=false
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --domain) [ "$#" -ge 2 ] || die "--domain 缺少参数"; explicit_domain=$2; shift 2 ;;
      --ingress) [ "$#" -ge 2 ] || die "--ingress 缺少参数"; explicit_ingress_mode=$2; shift 2 ;;
      --tag)
        case "${2:-}" in
          ''|--*)
            [ -z "$version" ] || die "--tag 无参数不能与显式 Release Tag 同时使用"
            tag_menu_requested=true
            shift
            ;;
          *)
            [ "$tag_menu_requested" = false ] \
              || die "--tag 无参数不能与显式 Release Tag 同时使用"
            if [ -n "$version" ] && [ "$version" != "$2" ]; then
              die "--tag 与 --version 不能指定不同版本"
            fi
            version=$2
            shift 2
            ;;
        esac
        ;;
      --version)
        [ "$#" -ge 2 ] || die "--version 缺少参数"
        [ "$tag_menu_requested" = false ] \
          || die "--tag 无参数不能与 --version 同时使用"
        if [ -n "$version" ] && [ "$version" != "$2" ]; then
          die "--tag 与 --version 不能指定不同版本"
        fi
        version=$2
        shift 2
        ;;
      --config) [ "$#" -ge 2 ] || die "--config 缺少参数"; config_file=$2; shift 2 ;;
      *) die "未知 run 参数：$1" ;;
    esac
  done
  require_root run
  deploy_root=$(resolve_operator_deploy_root "$config_file" "$STAGING_ROOT/runtime" /opt/codex-cpa-cluster) || return 1
  set_operator_deploy_root "$deploy_root" || return 1
  deploy_root=$CPAP_DEPLOY_ROOT
  ui_banner
  if [ -n "$version" ]; then
    validate_version "$version" || die "无效 Release Tag：$version"
  fi
  if [ "$tag_menu_requested" = true ]; then
    if choose_upgrade_release "$deploy_root" "$repository"; then
      version=$SELECTED_RELEASE_TAG
    else
      selection_status=$?
      [ "$selection_status" -eq 2 ] || return "$selection_status"
      return 0
    fi
  fi
  OPERATOR_PUBLIC_PORT=$(operator_public_port "$deploy_root")
  migrate_legacy_operator_state "$config_file"
  domain=$(resolve_deploy_domain "$config_file" "$explicit_domain")
  ingress_mode=$(resolve_deploy_ingress_mode \
    "$config_file" "$explicit_ingress_mode" "$deploy_root" "$domain")
  write_config "$config_file" "$domain" "$ingress_mode"
  if [ -e "$deploy_root" ]; then
    deploy_mode=升级
  else
    deploy_mode=首次安装
  fi
  ui_note "模式  $deploy_mode"
  ui_note "域名  $domain"
  ui_note "入口  $ingress_mode"
  ui_note "目录  $deploy_root"
  ui_run "检查系统环境" install_prerequisites "$ingress_mode"

  lock_file=${CPAP_LOCK_FILE:-/var/lock/cpa-deploy.lock}
  mkdir -p -- "$(dirname -- "$lock_file")"
  exec 9>"$lock_file"
  flock -n 9 || die "另一个 run.sh 正在运行"

  work_directory=$(mktemp -d "${TMPDIR:-/var/tmp}/cpa-deploy.XXXXXX")
  install_root=
  upgrade_pending=false
  rollback_directory="$work_directory/rollback"
  cleanup_files() {
    rm -rf -- "$work_directory"
    [ -z "$install_root" ] || [ ! -e "$install_root" ] || rm -rf -- "$install_root"
    [ -z "${backup_staging:-}" ] \
      || [ ! -e "$backup_staging" ] \
      || rm -rf -- "$backup_staging"
    [ -z "${backup_temporary:-}" ] \
      || [ ! -e "$backup_temporary" ] \
      || rm -f -- "$backup_temporary"
  }
  restore_release_metadata() {
    restore_ok=true
    for relative in target.env docker-compose.yml release-manifest.json; do
      cp -p -- "$rollback_directory/$relative" "$deploy_root/$relative" || restore_ok=false
    done
    [ "$restore_ok" = true ]
  }
  cleanup_on_exit() {
    exit_status=$?
    if [ "$upgrade_pending" = true ]; then
      printf '%s\n' "部署未提交，正在恢复原发布元数据" >&2
      restore_release_metadata \
        || printf '%s\n' "恢复原发布元数据失败；备份位于 ${backup:-未创建}" >&2
    fi
    cleanup_files
    trap - EXIT HUP INT TERM
    exit "$exit_status"
  }
  trap cleanup_on_exit EXIT
  trap 'exit 1' HUP INT TERM
  download_directory="$work_directory/download"
  extract_directory="$work_directory/release"
  ui_run "下载发布文件" download_release "$repository" "$version" "$download_directory"
  selected_version=$(cat "$download_directory/VERSION")
  ui_note "Release Tag  $selected_version"
  ui_run "校验发布文件" verify_release "$download_directory" "$selected_version"

  ui_step "同步统一部署脚本"
  if update_operator_script "$download_directory/run.sh"; then
    ui_done "部署脚本已更新，继续执行"
    cleanup_files
    trap - EXIT HUP INT TERM
    exec 9>&-
    exec "$SCRIPT_PATH" run --domain "$domain" --ingress "$ingress_mode" \
      --tag "$selected_version" --config "$config_file"
  fi
  ui_done "部署脚本已是当前版本"

  release_file="$download_directory/release-$selected_version.env"
  archive="$download_directory/$(release_archive_name "$release_file" "$selected_version")"
  ui_run "解压发布元数据" extract_release "$archive" "$extract_directory"
  [ -f "$extract_directory/docker-compose.yml" ] \
    && [ -f "$extract_directory/release-manifest.json" ] \
    || die "发布包缺少部署元数据"

  if [ -e "$deploy_root" ]; then
    summary_fresh=false
  else
    summary_fresh=true
  fi
  prepare_deployment_summary "$deploy_root" "$release_file" "$summary_fresh"

  if [ "$ingress_mode" = managed ]; then
    ui_step "配置 Nginx 与 HTTPS"
    configure_nginx_tls "$domain"
    ui_done "Nginx 与 HTTPS 已就绪"
    DEPLOY_SUMMARY_INGRESS="CPAP 托管；Nginx / HTTPS 已校验"
  else
    show_external_ingress_contract "$domain"
    DEPLOY_SUMMARY_INGRESS="外部托管；Nginx / Certbot 未修改"
  fi
  fresh=false
  backup=
  if [ -e "$deploy_root" ]; then
    ui_step "备份并准备现有环境"
    [ -d "$deploy_root" ] && [ ! -L "$deploy_root" ] \
      || die "部署根目录必须是普通目录：$deploy_root"
    target_initialized "$deploy_root" || die "部署根目录存在但状态不完整，拒绝覆盖：$deploy_root"
    validate_upgrade_target "$deploy_root"
    validate_optional_account_runtime_layout "$deploy_root"
    backup=$(backup_target "$deploy_root")
    ensure_account_runtime_layout "$deploy_root"
    mkdir -p "$rollback_directory"
    chmod 0700 "$rollback_directory"
    for relative in target.env docker-compose.yml release-manifest.json; do
      cp -p -- "$deploy_root/$relative" "$rollback_directory/$relative"
    done
    upgrade_pending=true
    install_release_metadata "$extract_directory" "$deploy_root"
    write_target_env "$release_file" "$selected_version" "$deploy_root/target.env" "$deploy_root"
    ui_done "现有环境备份完成"
    ui_note "备份  $backup"
    DEPLOY_SUMMARY_BACKUP="已创建 $backup"
  else
    ui_step "初始化全新运行环境"
    fresh=true
    parent=$(dirname -- "$deploy_root")
    mkdir -p -- "$parent"
    [ ! -L "$parent" ] || die "部署根目录父目录不能是符号链接：$parent"
    install_root=$(mktemp -d "$parent/.cpa-install.XXXXXX")
    chmod 0700 "$install_root"
    install_release_metadata "$extract_directory" "$install_root"
    write_target_env "$release_file" "$selected_version" "$install_root/target.env" "$deploy_root"
    control_image=$(release_value "$release_file" CPAP_CONTROL_IMAGE)
    ui_note "首次拉取镜像可能需要几分钟"
    docker pull --quiet "$control_image" >/dev/null
    config_directory=$(dirname -- "$config_file")
    pending_key="$config_directory/bootstrap-admin.key"
    [ ! -e "$pending_key" ] || die "已有待领取管理员凭据，拒绝覆盖：$pending_key"
    temporary_key=$(mktemp "$config_directory/.bootstrap-admin.XXXXXX")
    umask 077
    if ! docker run --rm \
      -v "$install_root:$install_root" \
      "$control_image" \
      /usr/local/bin/cpa-bootstrap --root "$install_root" --management-key-only >"$temporary_key"; then
      rm -f -- "$temporary_key"
      die "首次状态初始化失败"
    fi
    ensure_account_runtime_layout "$install_root"
    key_lines=$(awk 'NF { count++; value=$0 } END { if (count == 1) print value; else exit 1 }' "$temporary_key") \
      || { rm -f -- "$temporary_key"; die "首次管理员凭据输出无效"; }
    [ "${#key_lines}" -ge 12 ] && [ "${#key_lines}" -le 128 ] \
      || { rm -f -- "$temporary_key"; die "首次管理员凭据长度无效"; }
    chmod 0600 "$temporary_key"
    mv -- "$temporary_key" "$pending_key"
    mv -- "$install_root" "$deploy_root"
    install_root=
    ui_done "全新运行环境初始化完成"
    DEPLOY_SUMMARY_BACKUP="首次安装，不创建升级备份"
  fi

  # Read only the required value; unrelated target.env lines are not executed.
  upstream_network=$(deployment_upstream_network "$deploy_root/target.env")
  if ! docker network inspect "$upstream_network" >/dev/null 2>&1; then
    docker network create "$upstream_network" >/dev/null
  fi
  deployment_failed=false
  for action in config pull verify-images activate up-core up-writers smoke; do
    action_label=$(target_action_label "$action")
    if ! ui_run "$action_label" run_target_action "$deploy_root" "$action"; then
      ui_error "部署阶段失败：$action"
      deployment_failed=true
      break
    fi
    show_target_action_details "$action" "$deploy_root"
  done
  if [ "$deployment_failed" = true ]; then
    if [ "$fresh" = false ]; then
      ui_error "部署失败，正在恢复上一发布配置：$backup"
      restore_release_metadata || die "部署失败且原发布元数据恢复失败；备份位于 $backup"
      rollback_ok=true
      for action in pull verify-images activate up-core up-writers smoke; do
        action_label=$(target_action_label "$action")
        ui_run "回滚：$action_label" run_target_action "$deploy_root" "$action" \
          || rollback_ok=false
      done
      [ "$rollback_ok" = true ] || die "部署和自动回滚均失败；备份位于 $backup"
      upgrade_pending=false
      die "部署失败，已恢复上一版本；备份位于 $backup"
    fi
    die "首次部署失败；目标状态保留在 ${deploy_root}，可修复后重试"
  fi

  upgrade_pending=false
  write_initialized_marker "$deploy_root" "$selected_version"
  write_config "$config_file" "$domain" "$ingress_mode" "$deploy_root"
  prune_legacy_release_payload "$deploy_root"
  if [ "$ingress_mode" = managed ]; then
    ui_step "验证公网 HTTPS"
    external_status=$(curl --connect-timeout 10 --max-time 30 -sS -o /dev/null \
      -w '%{http_code}' "https://$domain/__health" || true)
    [ "$external_status" = 200 ] \
      || die "容器部署已完成，但 https://$domain/__health 返回 ${external_status:-连接失败}"
    ui_done "公网 HTTPS 健康检查通过"
  else
    ui_note "已跳过公网检查；请由现有反向代理验证 ${domain}/__health 返回 HTTP 200"
  fi
  remove_legacy_operator_script
  ui_complete "$selected_version" "$domain" "$deploy_root" "$ingress_mode"
  [ -z "$backup" ] || ui_note "升级前备份  $backup"
  pending_key="$(dirname -- "$config_file")/bootstrap-admin.key"
  if [ -f "$pending_key" ]; then
    if [ -t 1 ]; then
      claim_admin_key "$config_file"
    else
      ui_note "首次管理员凭据已安全保留；请在交互终端执行：sudo $SCRIPT_PATH admin-key claim"
    fi
  fi
  ui_note "以后升级直接执行：sudo $SCRIPT_PATH"
}

run_domain_set() {
  require_root domain set
  config_file=$DEFAULT_CONFIG_FILE
  confirmed=false
  [ "$#" -ge 1 ] || die "用法：sudo $SCRIPT_PATH domain set <新域名>"
  requested=$1
  shift
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --yes) confirmed=true; shift ;;
      --config) [ "$#" -ge 2 ] || die "--config 缺少参数"; config_file=$2; shift 2 ;;
      *) die "未知 domain set 参数：$1" ;;
    esac
  done
  migrate_legacy_operator_state "$config_file"
  domain=$(normalize_domain "$requested") || die "域名格式无效：$requested"
  current=
  current_ingress_mode=
  [ ! -e "$config_file" ] || current=$(config_domain "$config_file")
  if [ -e "$config_file" ] && config_has_explicit_ingress_mode "$config_file"; then
    current_ingress_mode=$(config_ingress_mode "$config_file")
  fi
  if [ -n "$current" ] && [ "$current" != "$domain" ] && [ "$confirmed" != true ]; then
    [ -t 0 ] || die "非交互修改域名必须添加 --yes"
    printf '确认将域名从 %s 修改为 %s？[y/N] ' "$current" "$domain" >&2
    IFS= read -r answer
    case "$answer" in y|Y|yes|YES) ;; *) die "已取消域名修改" ;; esac
  fi
  write_config "$config_file" "$domain" "$current_ingress_mode"
  printf '已记录域名：%s\n' "$domain"
}

run_ingress_set() {
  require_root ingress set
  config_file=$DEFAULT_CONFIG_FILE
  confirmed=false
  [ "$#" -ge 1 ] || die "用法：sudo $SCRIPT_PATH ingress set managed|external"
  requested=$1
  shift
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --yes) confirmed=true; shift ;;
      --config) [ "$#" -ge 2 ] || die "--config 缺少参数"; config_file=$2; shift 2 ;;
      *) die "未知 ingress set 参数：$1" ;;
    esac
  done
  migrate_legacy_operator_state "$config_file"
  [ -e "$config_file" ] || die "请先记录域名：sudo $SCRIPT_PATH domain set <域名>"
  domain=$(config_domain "$config_file")
  current=$(config_ingress_mode "$config_file")
  requested=$(validate_ingress_mode "$requested") \
    || die "入口模式无效：${requested}（仅支持 managed 或 external）"
  if [ "$current" != "$requested" ] && [ "$confirmed" != true ]; then
    [ -t 0 ] || die "非交互切换入口模式必须添加 --yes"
    printf '确认将入口模式从 %s 修改为 %s？[y/N] ' "$current" "$requested" >&2
    IFS= read -r answer
    case "$answer" in y|Y|yes|YES) ;; *) die "已取消入口模式修改" ;; esac
  fi
  write_config "$config_file" "$domain" "$requested"
  printf '已记录入口模式：%s\n' "$requested"
  if [ "$requested" = external ]; then
    printf '%s\n' 'CPAP 不会删除或修改既有 Nginx 站点；请先由你的反向代理接管该域名，再执行 run。'
  fi
}

operator_usage() {
  cat <<EOF
安装或升级：
  curl -fsSL $RUN_ASSET_URL | sudo sh

高级管理：
  sudo $SCRIPT_PATH --tag [RELEASE_TAG]
  sudo $SCRIPT_PATH run [--domain DOMAIN] [--ingress managed|external] [--tag [RELEASE_TAG]]
  sudo $SCRIPT_PATH domain set DOMAIN
  sudo $SCRIPT_PATH ingress set managed|external
  sudo $SCRIPT_PATH admin-key claim
EOF
}

ENTRY_COMMAND=${1:-run}
if [ "$STDIN_BOOTSTRAP" != true ]; then
  case "$ENTRY_COMMAND" in
    __target)
      ;;
    run)
      [ "$#" -eq 0 ] || shift
      run_install_or_upgrade "$@"
      exit 0
      ;;
    --domain|--ingress|--tag|--version|--config)
      run_install_or_upgrade "$@"
      exit 0
      ;;
    domain)
      shift
      subcommand=${1:-}
      [ "$subcommand" = set ] || die "用法：sudo $SCRIPT_PATH domain set <新域名>"
      shift
      run_domain_set "$@"
      exit 0
      ;;
    ingress)
      shift
      subcommand=${1:-}
      [ "$subcommand" = set ] || die "用法：sudo $SCRIPT_PATH ingress set managed|external"
      shift
      run_ingress_set "$@"
      exit 0
      ;;
    admin-key)
      shift
      subcommand=${1:-}
      [ "$subcommand" = claim ] || die "用法：sudo $SCRIPT_PATH admin-key claim"
      shift
      [ "$#" -eq 0 ] || die "admin-key claim 不接受额外参数"
      require_root admin-key claim
      migrate_legacy_operator_state "$DEFAULT_CONFIG_FILE"
      claim_admin_key "$DEFAULT_CONFIG_FILE"
      exit 0
      ;;
    help|-h|--help)
      operator_usage
      exit 0
      ;;
    *)
      die "未知命令：$ENTRY_COMMAND"
      ;;
  esac
fi

if [ "$STDIN_BOOTSTRAP" != true ]; then
  ROOT_DIR=${CPA_RELEASE_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}
  ACTION=${2:-config}
ENV_FILE=${CPA_ENV_FILE:-$ROOT_DIR/target.env}
RELEASE_COMPOSE_FILE="$ROOT_DIR/docker-compose.yml"

case "$ENV_FILE" in /*) ;; *) ENV_FILE="$ROOT_DIR/$ENV_FILE" ;; esac

[ -f "$ENV_FILE" ] && [ ! -L "$ENV_FILE" ] || {
  echo "Go target env file must be a regular non-symlink file: $ENV_FILE" >&2
  exit 1
}
[ -f "$RELEASE_COMPOSE_FILE" ] && [ ! -L "$RELEASE_COMPOSE_FILE" ] || {
  echo "Go release Compose file must be a regular non-symlink file: $RELEASE_COMPOSE_FILE" >&2
  exit 1
}

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

: "${CPA_CONTROL_IMAGE:?CPA_CONTROL_IMAGE is required}"
: "${CPA_WEB_IMAGE:?CPA_WEB_IMAGE is required}"
: "${CPA_GATEWAY_IMAGE:?CPA_GATEWAY_IMAGE is required}"
: "${CPA_EDGE_IMAGE:?CPA_EDGE_IMAGE is required}"
: "${CPA_DEPLOY_ROOT:?CPA_DEPLOY_ROOT is required}"
: "${CPA_PUBLIC_BIND_ADDRESS:=127.0.0.1}"
: "${CPA_PUBLIC_PROBE_HOST:=$CPA_PUBLIC_BIND_ADDRESS}"
: "${CPA_PUBLIC_PORT:?CPA_PUBLIC_PORT is required}"
: "${CPA_INTERNAL_PORT:?CPA_INTERNAL_PORT is required}"
: "${CPA_UPSTREAM_NETWORK:?CPA_UPSTREAM_NETWORK is required}"
: "${CPA_ACCOUNT_COMPOSE_PROJECT:?CPA_ACCOUNT_COMPOSE_PROJECT is required}"
: "${CPA_ACCOUNT_INSTANCE_NAME:?CPA_ACCOUNT_INSTANCE_NAME is required}"
: "${CPA_RUNTIME_OWNER:=codex-cpa}"
: "${CPA_OWNERSHIP_ACTIVATION_TTL:=2m}"
: "${CPA_ALLOW_EDGE_RECREATE:=false}"
: "${CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS:=1}"
: "${CPA_GATEWAY_FORCE_STOP_ON_DRAIN_TIMEOUT:=true}"
# Targets bootstrapped before v2.1.1 store the former 3600s default in target.env,
# and that file is sourced above, so the stored value would otherwise keep the old
# one-hour drain. Treat the legacy default as "use the shipped default"; any other
# operator value is still honoured.
if [ "$CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS" = "3600" ]; then
  CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS=1
fi
: "${CPA_CONFIRM_DEPLOY_ROOT:?CPA_CONFIRM_DEPLOY_ROOT must exactly repeat CPA_DEPLOY_ROOT}"


case "$CPA_DEPLOY_ROOT" in
  /*) ;;
  *) echo "CPA_DEPLOY_ROOT must be an absolute path" >&2; exit 1 ;;
esac
[ "$CPA_DEPLOY_ROOT" != / ] || {
  echo "CPA_DEPLOY_ROOT must not be the filesystem root" >&2
  exit 1
}
[ -d "$CPA_DEPLOY_ROOT" ] || {
  echo "CPA_DEPLOY_ROOT does not exist: $CPA_DEPLOY_ROOT" >&2
  exit 1
}
CANONICAL_DEPLOY_ROOT=$(CDPATH= cd -- "$CPA_DEPLOY_ROOT" && pwd -P)
[ "$CANONICAL_DEPLOY_ROOT" = "$CPA_DEPLOY_ROOT" ] || {
  echo "CPA_DEPLOY_ROOT must be canonical and must not traverse a symlink: $CPA_DEPLOY_ROOT" >&2
  exit 1
}
[ "$CPA_CONFIRM_DEPLOY_ROOT" = "$CPA_DEPLOY_ROOT" ] || {
  echo "CPA_CONFIRM_DEPLOY_ROOT does not match CPA_DEPLOY_ROOT" >&2
  exit 1
}

require_real_directory() {
  directory=$1
  description=$2
  [ -d "$directory" ] && [ ! -L "$directory" ] || {
    echo "$description must be an existing non-symlink directory: $directory" >&2
    exit 1
  }
  canonical_directory=$(CDPATH= cd -- "$directory" && pwd -P)
  [ "$canonical_directory" = "$directory" ] || {
    echo "$description must be canonical and must not traverse a symlink: $directory" >&2
    exit 1
  }
}

require_regular_file() {
  required_path=$1
  description=$2
  [ -f "$required_path" ] && [ ! -L "$required_path" ] || {
    echo "$description must be an existing regular non-symlink file: $required_path" >&2
    exit 1
  }
}

read_active_slot_file() {
  slot_file=$1
  slot_content=$(cat "$slot_file") || return 1
  case "$slot_content" in
    'set $active_gateway_backend gateway-blue:8317;') printf '%s\n' blue ;;
    'set $active_gateway_backend gateway-green:8317;') printf '%s\n' green ;;
    *) return 1 ;;
  esac
}

TARGET_COMPOSE_FILE="$CPA_DEPLOY_ROOT/docker-compose.yml"
MANIFEST_FILE="$CPA_DEPLOY_ROOT/release-manifest.json"
require_regular_file "$TARGET_COMPOSE_FILE" "Go target Compose file"
require_regular_file "$MANIFEST_FILE" "Go release manifest"
if ! cmp -s "$RELEASE_COMPOSE_FILE" "$TARGET_COMPOSE_FILE"; then
  echo "Go target Compose file does not match the selected release: $TARGET_COMPOSE_FILE" >&2
  exit 1
fi
COMPOSE_FILE=$TARGET_COMPOSE_FILE

for required_directory in \
  "$CPA_DEPLOY_ROOT/state" \
  "$CPA_DEPLOY_ROOT/state/gateway" \
  "$CPA_DEPLOY_ROOT/state/edge" \
  "$CPA_DEPLOY_ROOT/secrets" \
  "$CPA_DEPLOY_ROOT/auth" \
  "$CPA_DEPLOY_ROOT/configs" \
  "$CPA_DEPLOY_ROOT/management" \
  "$CPA_DEPLOY_ROOT/management/config" \
  "$CPA_DEPLOY_ROOT/management/config/static" \
  "$CPA_DEPLOY_ROOT/logs" \
  "$CPA_DEPLOY_ROOT/logs/gateway"
do
  require_real_directory "$required_directory" "Go target runtime directory"
done
for required_file in \
  "$CPA_DEPLOY_ROOT/state/control-plane.sqlite3" \
  "$CPA_DEPLOY_ROOT/state/usage.sqlite3" \
  "$CPA_DEPLOY_ROOT/secrets/control-plane.key" \
  "$CPA_DEPLOY_ROOT/state/edge/active-gateway.conf"
do
  require_regular_file "$required_file" "Go target runtime file"
done
if ! read_active_slot_file "$CPA_DEPLOY_ROOT/state/edge/active-gateway.conf" >/dev/null; then
  echo "Go target active Gateway selection must contain exactly blue or green" >&2
  exit 1
fi

validate_port() {
  port_name=$1
  port_value=$2
  case "$port_value" in
    ""|0|*[!0-9]*)
      echo "$port_name must be an integer between 1 and 65535" >&2
      exit 1
      ;;
  esac
  [ "$port_value" -le 65535 ] || {
    echo "$port_name must be an integer between 1 and 65535" >&2
    exit 1
  }
}

validate_content_image() {
  image_name=$1
  image_value=$2
  digest=${image_value##*:sha256-}
  prefix=${image_value%:sha256-*}
  if [ "$digest" = "$image_value" ] || [ "$prefix" = "$image_value" ] \
    || [ -z "$prefix" ] || [ "${#digest}" -ne 64 ]; then
    echo "$image_name must use the immutable :sha256-<64 lowercase hex> tag from release metadata" >&2
    exit 1
  fi
  case "$digest" in
    *[!0-9a-f]*)
      echo "$image_name must use the immutable :sha256-<64 lowercase hex> tag from release metadata" >&2
      exit 1
      ;;
  esac
}

validate_port CPA_PUBLIC_PORT "$CPA_PUBLIC_PORT"
validate_port CPA_INTERNAL_PORT "$CPA_INTERNAL_PORT"
[ "$CPA_PUBLIC_PORT" != "$CPA_INTERNAL_PORT" ] || {
  echo "CPA_PUBLIC_PORT and CPA_INTERNAL_PORT must be different" >&2
  exit 1
}
validate_content_image CPA_CONTROL_IMAGE "$CPA_CONTROL_IMAGE"
validate_content_image CPA_WEB_IMAGE "$CPA_WEB_IMAGE"
validate_content_image CPA_GATEWAY_IMAGE "$CPA_GATEWAY_IMAGE"
validate_content_image CPA_EDGE_IMAGE "$CPA_EDGE_IMAGE"
case "$CPA_ALLOW_EDGE_RECREATE" in
  true|false) ;;
  *) echo "CPA_ALLOW_EDGE_RECREATE must be true or false" >&2; exit 1 ;;
esac
case "$CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS" in
  ""|0|*[!0-9]*)
    echo "CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS must be a positive integer" >&2
    exit 1
    ;;
esac
case "$CPA_GATEWAY_FORCE_STOP_ON_DRAIN_TIMEOUT" in
  true|false) ;;
  *) echo "CPA_GATEWAY_FORCE_STOP_ON_DRAIN_TIMEOUT must be true or false" >&2; exit 1 ;;
esac

compose() {
  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

image_label() {
  docker image inspect --format "{{index .Config.Labels \"$2\"}}" "$1" 2>/dev/null || true
}

expected_digest() {
  docker run --rm \
    -v "$MANIFEST_FILE:/release-manifest.json:ro" \
    "$CPA_CONTROL_IMAGE" \
    /usr/local/bin/cpa-releasectl manifest get \
    --manifest /release-manifest.json \
    --component "$1"
}

image_tag_digest() {
  printf '%s\n' "${1##*:sha256-}"
}

validate_image() {
  image=$1
  component=$2
  expected=$3
  actual_component=$(image_label "$image" io.codex-cpa.component)
  actual_digest=$(image_label "$image" io.codex-cpa.component-digest)
  actual_source_digest=$(image_label "$image" io.codex-cpa.source-digest)
  if [ "$actual_component" != "$component" ] || [ "$actual_digest" != "$expected" ] \
    || [ "$actual_source_digest" != "$expected" ]; then
    echo "Go image label mismatch: component=$component image=$image" >&2
    exit 1
  fi
}

validate_tagged_image() {
  tagged_image=$1
  tagged_component=$2
  validate_image "$tagged_image" "$tagged_component" "$(image_tag_digest "$tagged_image")"
}

validate_release_manifest_images() {
  for pair in \
    "control|$CPA_CONTROL_IMAGE" \
    "web|$CPA_WEB_IMAGE" \
    "gateway|$CPA_GATEWAY_IMAGE" \
    "edge|$CPA_EDGE_IMAGE"
  do
    component=${pair%%|*}
    image=${pair#*|}
    manifest_digest=$(expected_digest "$component")
    tag_digest=$(image_tag_digest "$image")
    [ "$manifest_digest" = "$tag_digest" ] || {
      echo "Go release manifest does not match the selected image tag: component=$component image=$image" >&2
      exit 1
    }
    validate_image "$image" "$component" "$manifest_digest"
  done
}

pull_images() {
  require_regular_file "$MANIFEST_FILE" "Go release manifest"
  for pair in \
    "control|$CPA_CONTROL_IMAGE" \
    "web|$CPA_WEB_IMAGE" \
    "gateway|$CPA_GATEWAY_IMAGE" \
    "edge|$CPA_EDGE_IMAGE"
  do
    component=${pair%%|*}
    image=${pair#*|}
    docker pull --quiet "$image" >/dev/null
    validate_tagged_image "$image" "$component"
  done
  # Only execute releasectl from Control after its non-executing image labels
  # have matched the immutable source-digest tag.
  validate_release_manifest_images
}

verify_images() {
  require_regular_file "$MANIFEST_FILE" "Go release manifest"
  for pair in \
    "control|$CPA_CONTROL_IMAGE" \
    "web|$CPA_WEB_IMAGE" \
    "gateway|$CPA_GATEWAY_IMAGE" \
    "edge|$CPA_EDGE_IMAGE"
  do
    component=${pair%%|*}
    image=${pair#*|}
    validate_tagged_image "$image" "$component"
  done
  validate_release_manifest_images
  require_edge_recreate_policy
  echo "Go target images verified"
}

ownership_json() {
  docker run --rm \
    -v "$CPA_DEPLOY_ROOT:$CPA_DEPLOY_ROOT" \
    "$CPA_CONTROL_IMAGE" \
    /usr/local/bin/cpa-ownership \
    --root "$CPA_DEPLOY_ROOT" \
    status
}

ownership_status_field() {
  docker run --rm \
    -v "$CPA_DEPLOY_ROOT:$CPA_DEPLOY_ROOT" \
    "$CPA_CONTROL_IMAGE" \
    /usr/local/bin/cpa-ownership \
    --root "$CPA_DEPLOY_ROOT" \
    status --field "$1"
}

require_active_owner() {
  active=$(ownership_status_field active)
  owner=$(ownership_status_field owner)
  if [ "$active" != true ] || [ "$owner" != "$CPA_RUNTIME_OWNER" ]; then
    echo "Go runtime ownership is not active for $CPA_RUNTIME_OWNER" >&2
    exit 1
  fi
}

require_container_network() {
  container=$1
  network=$2
  networks=$(docker inspect --format '{{json .NetworkSettings.Networks}}' "$container")
  printf '%s' "$networks" | grep -Fq "\"$network\":" || {
    echo "Go container is missing required network: container=$container network=$network" >&2
    return 1
  }
}

ensure_target_network() {
  container=$1
  project=$2
  service=$3
  network=$4

  actual_project=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$container")
  actual_service=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$container")
  [ "$actual_project" = "$project" ] && [ "$actual_service" = "$service" ] || {
    echo "refusing network repair outside exact Go service: container=$container project=$actual_project service=$actual_service" >&2
    return 1
  }
  actual_network=$(docker network inspect --format '{{.Name}}' "$network")
  [ "$actual_network" = "$network" ] || {
    echo "refusing network repair for unexpected network: expected=$network actual=$actual_network" >&2
    return 1
  }
  if require_container_network "$container" "$network" 2>/dev/null; then
    return
  fi

  docker network connect \
    --alias "$service" \
    --alias "$container" \
    "$network" \
    "$container"
  require_container_network "$container" "$network"
  echo "restored Go target network: container=$container network=$network"
}

wait_target_container() {
  wait_container=$1
  wait_seconds=${CPA_SERVICE_WAIT_SECONDS:-120}
  case "$wait_seconds" in
    ""|0|*[!0-9]*)
      echo "CPA_SERVICE_WAIT_SECONDS must be a positive integer" >&2
      return 1
      ;;
  esac

  wait_attempt=0
  wait_status=missing
  while [ "$wait_attempt" -lt "$wait_seconds" ]; do
    wait_status=$(docker inspect --format \
      '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' \
      "$wait_container" 2>/dev/null || true)
    case "$wait_status" in
      healthy|running)
        return 0
        ;;
      exited|dead)
        echo "Go target container stopped before readiness: container=$wait_container status=$wait_status" >&2
        return 1
        ;;
    esac
    wait_attempt=$((wait_attempt + 1))
    sleep 1
  done

  echo "Go target container did not become ready: container=$wait_container status=$wait_status" >&2
  return 1
}

require_container_port() {
  container=$1
  container_port=$2
  host_ip=$3
  host_port=$4
  bindings=$(docker inspect --format "{{range (index .NetworkSettings.Ports \"$container_port\")}}{{println .HostIp .HostPort}}{{end}}" "$container")
  printf '%s\n' "$bindings" | grep -Fxq "$host_ip $host_port" || {
    echo "Go container is missing required port binding: container=$container binding=$host_ip:$host_port->$container_port" >&2
    return 1
  }
}

container_exists() {
  docker inspect "$1" >/dev/null 2>&1
}

container_running() {
  [ "$(docker inspect --format '{{.State.Running}}' "$1" 2>/dev/null || true)" = true ]
}

container_image_id() {
  docker inspect --format '{{.Image}}' "$1"
}

target_image_id() {
  docker image inspect --format '{{.Id}}' "$1"
}

container_config_hash() {
  hash=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.config-hash"}}' "$1") || {
    echo "unable to read running Compose hash: container=$1" >&2
    return 1
  }
  validate_sha256_value "$hash" "running Compose hash for $1" || return 1
  printf '%s\n' "$hash"
}

service_config_hash() {
  service=$1
  case "$service" in
    usage-collector|quota|account-failover|log-maintenance)
      output=$(compose --profile writers config --hash "$service")
      ;;
    notifications)
      output=$(compose --profile writers --profile external-effects config --hash "$service")
      ;;
    *)
      output=$(compose config --hash "$service")
      ;;
  esac || {
    echo "unable to calculate target Compose hash: service=$service" >&2
    return 1
  }
  hash=$(printf '%s\n' "$output" | awk -v service="$service" '$1 == service { if (found) exit 2; found=1; hash=$2 } END { if (!found) exit 1; print hash }') || {
    echo "target Compose hash output is missing or ambiguous: service=$service" >&2
    return 1
  }
  validate_sha256_value "$hash" "target Compose hash for $service" || return 1
  printf '%s\n' "$hash"
}

validate_sha256_value() {
  digest_value=$1
  digest_description=$2
  if [ "${#digest_value}" -ne 64 ]; then
    echo "$digest_description must be a 64-character lowercase hexadecimal digest" >&2
    return 1
  fi
  case "$digest_value" in
    *[!0-9a-f]*)
      echo "$digest_description must be a 64-character lowercase hexadecimal digest" >&2
      return 1
      ;;
  esac
}

service_recreate_state() {
  container=$1
  service=$2
  image=$3
  running_image=$(container_image_id "$container") || {
    echo "unable to read running image identity: container=$container" >&2
    return 1
  }
  desired_image=$(target_image_id "$image") || {
    echo "unable to read target image identity: image=$image" >&2
    return 1
  }
  [ -n "$running_image" ] && [ -n "$desired_image" ] || {
    echo "running and target image identities must not be empty: container=$container image=$image" >&2
    return 1
  }
  if [ "$running_image" != "$desired_image" ]; then
    printf '%s\n' true
    return
  fi
  running_hash=$(container_config_hash "$container") || return 1
  desired_hash=$(service_config_hash "$service") || return 1
  if [ "$running_hash" != "$desired_hash" ]; then
    printf '%s\n' true
  else
    printf '%s\n' false
  fi
}

require_service_current() {
  current_container=$1
  current_service=$2
  current_image=$3
  recreate_state=$(service_recreate_state "$current_container" "$current_service" "$current_image") || return 1
  [ "$recreate_state" = false ] || {
    echo "Go service did not converge to its exact target image and Compose configuration: service=$current_service container=$current_container" >&2
    return 1
  }
}

require_exact_compose_service() {
  container=$1
  project=$2
  service=$3
  actual_project=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$container")
  actual_service=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$container")
  [ "$actual_project" = "$project" ] && [ "$actual_service" = "$service" ] || {
    echo "refusing Go rollout for unexpected container identity: container=$container project=$actual_project service=$actual_service" >&2
    return 1
  }
}

gateway_inflight() {
  gateway_container=$1
  stats=$(docker exec "$gateway_container" wget -qO- http://127.0.0.1:8319/__stats) || {
    echo "unable to read Go Gateway in-flight statistics: container=$gateway_container" >&2
    return 1
  }
  printf '%s' "$stats" | awk '
    { payload = payload $0 }
    END {
      gsub(/[[:space:]]/, "", payload)
      if (payload == "[]") {
        print 0
        exit
      }
      if (payload !~ /^\[\{.*\}\]$/) {
        exit 2
      }
      object_count = gsub(/\{/, "{", payload)
      remaining = payload
      field_count = 0
      total = 0
      while (match(remaining, /"inflight":[0-9]+/)) {
        field = substr(remaining, RSTART, RLENGTH)
        sub(/^"inflight":/, "", field)
        total += field + 0
        field_count++
        remaining = substr(remaining, RSTART + RLENGTH)
      }
      if (field_count == 0 || field_count != object_count) {
        exit 2
      }
      print total
    }
  ' || {
    echo "Go Gateway returned invalid in-flight statistics: container=$gateway_container" >&2
    return 1
  }
}

wait_gateway_drain() {
  gateway_container=$1
  elapsed=0
  while [ "$elapsed" -lt "$CPA_GATEWAY_DRAIN_TIMEOUT_SECONDS" ]; do
    inflight=$(gateway_inflight "$gateway_container") || return 1
    case "$inflight" in
      ""|*[!0-9]*)
        echo "Go Gateway returned a non-numeric in-flight count: container=$gateway_container" >&2
        return 1
        ;;
    esac
    if [ "$inflight" -eq 0 ]; then
      echo "Go Gateway drained: container=$gateway_container"
      return
    fi
    elapsed=$((elapsed + 1))
    sleep 1
  done
  if [ "$CPA_GATEWAY_FORCE_STOP_ON_DRAIN_TIMEOUT" = true ]; then
    gateway_slot=${gateway_container##*-gateway-}
    edge_slot_port=${ROLLOUT_EDGE_INTERNAL_PORT:-$CPA_INTERNAL_PORT}
    current_edge_slot=$(curl --noproxy '*' --connect-timeout 2 --max-time 5 -fsS \
      "http://127.0.0.1:$edge_slot_port/__internal/edge/slot" 2>/dev/null) || {
      echo "refusing to force-stop Gateway without a verified Edge slot: container=$gateway_container" >&2
      return 1
    }
    if [ "$current_edge_slot" = "$gateway_slot" ]; then
      echo "refusing to force-stop the active Gateway slot: container=$gateway_container slot=$current_edge_slot" >&2
      return 1
    fi
    case "$gateway_slot:$current_edge_slot" in
      blue:green|green:blue) ;;
      *)
        echo "refusing to force-stop Gateway without a verified Edge slot: container=$gateway_container" >&2
        return 1
        ;;
    esac
    echo "Go Gateway drain timed out; force-stopping existing requests: container=$gateway_container inflight=$inflight" >&2
    if docker stop --time 0 "$gateway_container" >/dev/null; then
      echo "Go Gateway force-stopped after drain timeout: container=$gateway_container" >&2
      return 0
    fi
    echo "Go Gateway force-stop failed: container=$gateway_container" >&2
    return 1
  fi
  echo "Go Gateway drain timed out without terminating existing requests: container=$gateway_container inflight=$inflight" >&2
  return 1
}

wait_edge_slot() {
  expected_slot=$1
  edge_slot_port=${ROLLOUT_EDGE_INTERNAL_PORT:-$CPA_INTERNAL_PORT}
  elapsed=0
  while [ "$elapsed" -lt 30 ]; do
    actual_slot=$(curl --noproxy '*' -fsS "http://127.0.0.1:$edge_slot_port/__internal/edge/slot" 2>/dev/null || true)
    if [ "$actual_slot" = "$expected_slot" ]; then
      return
    fi
    elapsed=$((elapsed + 1))
    sleep 1
  done
  echo "Go Edge did not activate Gateway slot: expected=$expected_slot actual=${actual_slot:-unavailable}" >&2
  return 1
}

container_bound_port() {
  container=$1
  container_port=$2
  bindings=$(docker inspect --format "{{range (index .NetworkSettings.Ports \"$container_port\")}}{{println .HostIp .HostPort}}{{end}}" "$container")
  printf '%s\n' "$bindings" | awk '$1 == "127.0.0.1" { print $2; exit }'
}

write_gateway_slot_file() {
  slot=$1
  selection_dir="$CPA_DEPLOY_ROOT/state/edge"
  selection_file="$selection_dir/active-gateway.conf"
  [ -d "$selection_dir" ] && [ ! -L "$selection_dir" ] \
    && [ -f "$selection_file" ] && [ ! -L "$selection_file" ] || {
      echo "active Gateway selection must be an existing regular file in a non-symlink directory" >&2
      return 1
    }
  temporary_selection=$(mktemp "$selection_dir/.active-gateway.XXXXXX")
  case "$slot" in
    blue|green) ;;
    *) rm -f -- "$temporary_selection"; echo "invalid Gateway slot: $slot" >&2; return 1 ;;
  esac
  if ! printf 'set $active_gateway_backend gateway-%s:8317;\n' "$slot" >"$temporary_selection" \
    || ! chmod 0644 "$temporary_selection" \
    || ! mv -f -- "$temporary_selection" "$selection_file"; then
    rm -f -- "$temporary_selection"
    return 1
  fi
}

switch_gateway_slot() {
  slot=$1
  selection_file="$CPA_DEPLOY_ROOT/state/edge/active-gateway.conf"
  previous_slot=$(read_active_slot_file "$selection_file") || {
    echo "cannot switch from an invalid active Gateway selection" >&2
    return 1
  }
  [ "$previous_slot" != "$slot" ] || return 0
  write_gateway_slot_file "$slot" || return 1
  if ! wait_edge_slot "$slot"; then
    if write_gateway_slot_file "$previous_slot" && wait_edge_slot "$previous_slot"; then
      echo "Go Edge slot switch failed and was rolled back to Gateway $previous_slot" >&2
    else
      echo "Go Edge slot switch and rollback both failed; manual recovery is required" >&2
    fi
    return 1
  fi
  echo "Go Edge switched new requests to Gateway $slot"
}

require_edge_recreate_policy() {
  edge_container=${CPA_INSTANCE_NAME:-codex-cpa}-edge
  container_exists "$edge_container" || return 0
  require_exact_compose_service "$edge_container" "${CPA_COMPOSE_PROJECT_NAME:-codex-cpa}" edge
  edge_recreate=$(service_recreate_state "$edge_container" edge "$CPA_EDGE_IMAGE") || return 1
  [ "$edge_recreate" = true ] || return 0
  [ "$CPA_ALLOW_EDGE_RECREATE" = true ] \
    && [ "${CPA_CONFIRM_EDGE_MAINTENANCE:-}" = "$CPA_DEPLOY_ROOT" ] || {
      echo "changed Edge image or configuration requires CPA_ALLOW_EDGE_RECREATE=true and CPA_CONFIRM_EDGE_MAINTENANCE=$CPA_DEPLOY_ROOT" >&2
      return 1
    }
}

start_gateway_service() {
  slot=$1
  project=$2
  control_network=$3
  service="gateway-$slot"
  container="${CPA_INSTANCE_NAME:-codex-cpa}-$service"
  compose up -d --no-deps "$service"
  ensure_target_network "$container" "$project" "$service" "$control_network"
  ensure_target_network "$container" "$project" "$service" "$CPA_UPSTREAM_NETWORK"
  wait_target_container "$container"
  require_service_current "$container" "$service" "$CPA_GATEWAY_IMAGE"
  docker exec "$container" wget -qO- http://127.0.0.1:8319/__internal/ready >/dev/null
}

require_core_topology() {
  instance=${CPA_INSTANCE_NAME:-codex-cpa}
  project=${CPA_COMPOSE_PROJECT_NAME:-codex-cpa}
  control_network="${project}_control"
  ingress_network="${project}_ingress"

  for container in "$instance-gateway-blue" "$instance-gateway-green" "$instance-admin"; do
    require_container_network "$container" "$control_network"
    require_container_network "$container" "$CPA_UPSTREAM_NETWORK"
  done
  require_container_network "$instance-web" "$control_network"
  require_container_network "$instance-edge" "$control_network"
  require_container_network "$instance-edge" "$ingress_network"
  require_container_port "$instance-edge" "8317/tcp" "$CPA_PUBLIC_BIND_ADDRESS" "$CPA_PUBLIC_PORT"
  require_container_port "$instance-edge" "8319/tcp" "127.0.0.1" "$CPA_INTERNAL_PORT"
}

activate_owner() {
  status=$(ownership_json)
  found=$(ownership_status_field found)
  active=$(ownership_status_field active)
  owner=$(ownership_status_field owner)
  generation=$(ownership_status_field generation)
  if [ "$active" = true ]; then
    if [ "$owner" = "$CPA_RUNTIME_OWNER" ]; then
      printf '%s\n' "$status"
      return
    fi
    echo "runtime ownership is still active for another writer: $owner generation=$generation" >&2
    exit 1
  fi
  set -- docker run --rm \
    -v "$CPA_DEPLOY_ROOT:$CPA_DEPLOY_ROOT" \
    "$CPA_CONTROL_IMAGE" \
    /usr/local/bin/cpa-ownership \
    --root "$CPA_DEPLOY_ROOT" \
    --ttl "$CPA_OWNERSHIP_ACTIVATION_TTL" \
    activate \
    --owner "$CPA_RUNTIME_OWNER" \
    --confirm-owner "$CPA_RUNTIME_OWNER"
  if [ "$found" = true ]; then
    set -- "$@" --expected-owner "$owner" --expected-generation "$generation"
  else
    case "${CPA_BOOTSTRAP_MODE:-}" in
      isolated-test)
        set -- "$@" --allow-empty-bootstrap
        ;;
      controlled-cutover)
        [ "${CPA_CONFIRM_WRITERS_STOPPED:-}" = "$CPA_DEPLOY_ROOT" ] || {
          echo "controlled cutover requires CPA_CONFIRM_WRITERS_STOPPED=$CPA_DEPLOY_ROOT" >&2
          exit 1
        }
        set -- "$@" --confirm-existing-writers-stopped "writers-stopped:$CPA_DEPLOY_ROOT"
        ;;
      *)
        echo "empty ownership history requires CPA_BOOTSTRAP_MODE=isolated-test or controlled-cutover" >&2
        exit 1
        ;;
    esac
  fi
  "$@"
}

smoke() {
  public_url="http://$CPA_PUBLIC_PROBE_HOST:$CPA_PUBLIC_PORT"
  internal_url="http://127.0.0.1:$CPA_INTERNAL_PORT"
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/__health")" = 200 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/v1/models")" = 401 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url/__internal/snapshots")" = 404 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$internal_url/__internal/snapshots")" = 200 ]
  [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$internal_url/__internal/ready")" = 200 ]
  # The HTML routes are served by Web without touching Admin. The public site
  # configuration proves the Edge -> Web -> Admin path as well, so a missing
  # target control network cannot pass smoke with static pages alone.
  for path in / /admin/ /usage/ /native/ /site-config.json; do
    [ "$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "$public_url$path")" = 200 ]
  done
  echo "Go target smoke passed"
}

case "$ACTION" in
  pull)
    pull_images
    ;;
  verify-images)
    verify_images
    ;;
  config)
    compose --profile writers --profile external-effects config --quiet
    ;;
  ownership-status)
    ownership_json
    ;;
  activate)
    activate_owner
    ;;
  up-core)
    require_active_owner
    instance=${CPA_INSTANCE_NAME:-codex-cpa}
    project=${CPA_COMPOSE_PROJECT_NAME:-codex-cpa}
    control_network="${project}_control"
    ingress_network="${project}_ingress"
    require_edge_recreate_policy
    edge_container="$instance-edge"
    if container_exists "$edge_container" && container_running "$edge_container"; then
      require_exact_compose_service "$edge_container" "$project" edge
      ROLLOUT_EDGE_INTERNAL_PORT=$(container_bound_port "$edge_container" "8319/tcp")
      [ -n "$ROLLOUT_EDGE_INTERNAL_PORT" ] || {
        echo "running Edge has no loopback internal port binding" >&2
        exit 1
      }
      export ROLLOUT_EDGE_INTERNAL_PORT
      active_slot=$(curl --noproxy '*' -fsS "http://127.0.0.1:$ROLLOUT_EDGE_INTERNAL_PORT/__internal/edge/slot")
      case "$active_slot" in
        blue) inactive_slot=green ;;
        green) inactive_slot=blue ;;
        *) echo "running Edge returned invalid active Gateway slot: $active_slot" >&2; exit 1 ;;
      esac
      inactive_container="$instance-gateway-$inactive_slot"
      if container_exists "$inactive_container" && container_running "$inactive_container"; then
        require_exact_compose_service "$inactive_container" "$project" "gateway-$inactive_slot"
        # A previous rollout may have switched away from this slot and timed
        # out while preserving a long SSE. Drain it first; the configured
        # timeout policy may force-stop the old container before recreation.
        wait_gateway_drain "$inactive_container"
      fi
      start_gateway_service "$inactive_slot" "$project" "$control_network"

      active_container="$instance-gateway-$active_slot"
      require_exact_compose_service "$active_container" "$project" "gateway-$active_slot"
      active_recreate=$(service_recreate_state "$active_container" "gateway-$active_slot" "$CPA_GATEWAY_IMAGE") || exit 1
      if [ "$active_recreate" = true ]; then
        switch_gateway_slot "$inactive_slot"
        if container_running "$active_container"; then
          wait_gateway_drain "$active_container"
        fi
        start_gateway_service "$active_slot" "$project" "$control_network"
      fi
    else
      start_gateway_service blue "$project" "$control_network"
      start_gateway_service green "$project" "$control_network"
    fi

    # Admin and Web are not on the model data path. Start them one at a time so
    # topology repair and readiness failures remain attributable.
    for service in admin web; do
      compose up -d --no-deps "$service"
      container="$instance-$service"
      ensure_target_network "$container" "$project" "$service" "$control_network"
      if [ "$service" = admin ]; then
        ensure_target_network "$container" "$project" "$service" "$CPA_UPSTREAM_NETWORK"
      fi
      wait_target_container "$container"
      if [ "$service" = admin ]; then
        require_service_current "$container" "$service" "$CPA_CONTROL_IMAGE"
      else
        require_service_current "$container" "$service" "$CPA_WEB_IMAGE"
      fi
    done

    if container_exists "$edge_container"; then
      edge_recreate=$(service_recreate_state "$edge_container" edge "$CPA_EDGE_IMAGE") || exit 1
      if [ "$edge_recreate" = true ]; then
        compose up -d --no-deps --force-recreate edge
      else
        compose up -d --no-deps --no-recreate edge
      fi
    else
      compose up -d --no-deps edge
    fi
    ensure_target_network "$edge_container" "$project" edge "$control_network"
    ensure_target_network "$edge_container" "$project" edge "$ingress_network"
    wait_target_container "$edge_container"
    require_service_current "$edge_container" edge "$CPA_EDGE_IMAGE"
    require_core_topology
    ;;
  up-writers)
    require_active_owner
    instance=${CPA_INSTANCE_NAME:-codex-cpa}
    project=${CPA_COMPOSE_PROJECT_NAME:-codex-cpa}
    control_network="${project}_control"
    for pair in \
      "quota|true" \
      "usage-collector|true" \
      "account-failover|true" \
      "log-maintenance|false" \
      "notifications|true"
    do
      service=${pair%%|*}
      needs_upstream=${pair#*|}
      container="$instance-$service"
      if [ "$service" = notifications ]; then
        # Older release archives kept notifications in external-effects.
        compose --profile writers --profile external-effects up -d --no-deps "$service"
      else
        compose --profile writers up -d --no-deps "$service"
      fi
      ensure_target_network "$container" "$project" "$service" "$control_network"
      if [ "$needs_upstream" = true ]; then
        ensure_target_network "$container" "$project" "$service" "$CPA_UPSTREAM_NETWORK"
      fi
      wait_target_container "$container"
      require_service_current "$container" "$service" "$CPA_CONTROL_IMAGE"
    done
    ;;
  up-notifications)
    require_active_owner
    instance=${CPA_INSTANCE_NAME:-codex-cpa}
    project=${CPA_COMPOSE_PROJECT_NAME:-codex-cpa}
    control_network="${project}_control"
    container="$instance-notifications"
    compose --profile writers --profile external-effects up -d --no-deps notifications
    ensure_target_network "$container" "$project" notifications "$control_network"
    ensure_target_network "$container" "$project" notifications "$CPA_UPSTREAM_NETWORK"
    wait_target_container "$container"
    require_service_current "$container" notifications "$CPA_CONTROL_IMAGE"
    ;;
  smoke)
    smoke
    ;;
  ps)
    compose --profile writers --profile external-effects ps
    ;;
  down)
    compose --profile writers --profile external-effects down --remove-orphans
    ;;
  *)
    echo "unsupported action: $ACTION" >&2
    exit 1
    ;;
esac
fi

if [ "$STDIN_BOOTSTRAP" = true ]; then
  bootstrap_from_stdin "$@"
fi
