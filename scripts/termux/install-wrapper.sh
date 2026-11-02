#!/data/data/com.termux/files/usr/bin/bash
# One-command installer for the Reasonix Termux permission wrappers.
#
# Default (safer): install reasonix-full + reasonix-raw beside the real binary.
#   curl -fsSL https://raw.githubusercontent.com/wumingmr/reasonix_android_custon/master/scripts/termux/install-wrapper.sh | bash
#
# Make `reasonix` itself default to Full access:
#   curl -fsSL URL | bash -s -- --default-full
#
# Remove only files managed by this installer:
#   curl -fsSL URL | bash -s -- --uninstall
set -euo pipefail

REPO="${REASONIX_WRAPPER_REPO:-wumingmr/reasonix_android_custon}"
REF="${REASONIX_WRAPPER_REF:-master}"
BASE_URL="${REASONIX_WRAPPER_BASE_URL:-https://raw.githubusercontent.com/$REPO/$REF/scripts/termux}"
PREFIX_DIR="${PREFIX:-/data/data/com.termux/files/usr}"
REAL="$PREFIX_DIR/bin/reasonix"
HELPER_DIR="$PREFIX_DIR/bin"
HOME_BIN="$HOME/bin"
MODE="safe"

log() { printf '[reasonix-wrapper] %s\n' "$*"; }
die() { printf '[reasonix-wrapper] 错误: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'EOF'
用法: install-wrapper.sh [--default-full|--uninstall]

  无参数          安装 reasonix-full 和 reasonix-raw（不覆盖 reasonix）
  --default-full  另装 ~/bin/reasonix，使 reasonix 默认 danger-full-access
  --uninstall     仅删除本安装器管理的 wrapper，不删除真实二进制
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --default-full) MODE="default-full" ;;
    --uninstall) MODE="uninstall" ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "未知参数: $1" ;;
  esac
  shift
done

is_managed() {
  local path="$1"
  [ -f "$path" ] || return 1
  grep -qE '^(# Termux wrapper: interactive/run commands default to danger-full-access\.|# Invoke the real Termux Reasonix binary without injecting a permission mode\.)$' "$path"
}

remove_managed() {
  local path="$1"
  if is_managed "$path"; then
    rm -f "$path"
    log "已删除 $path"
  elif [ -e "$path" ]; then
    log "保留非本安装器管理的文件: $path"
  fi
}

if [ "$MODE" = "uninstall" ]; then
  remove_managed "$HELPER_DIR/reasonix-full"
  remove_managed "$HELPER_DIR/reasonix-raw"
  remove_managed "$HOME_BIN/reasonix"
  log "卸载完成；真实二进制 $REAL 未改动"
  exit 0
fi

[ -x "$REAL" ] || die "真实二进制不存在或不可执行: $REAL。请先安装 Reasonix Android 二进制。"
command -v curl >/dev/null 2>&1 || die "需要 curl；请先运行: pkg install curl"

TMP_ROOT="${REASONIX_TMPDIR:-$HOME/tmp}"
mkdir -p "$TMP_ROOT"
WORK="$(mktemp -d "$TMP_ROOT/reasonix-wrapper-XXXXXX")"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

fetch() {
  local name="$1" out="$2"
  curl -fsSL --retry 3 --connect-timeout 15 "$BASE_URL/$name" -o "$out" || \
    die "下载失败: $BASE_URL/$name"
}

fetch reasonix-wrapper "$WORK/reasonix-wrapper"
fetch reasonix-raw "$WORK/reasonix-raw"

grep -q '^# Termux wrapper: interactive/run commands default to danger-full-access\.$' "$WORK/reasonix-wrapper" || \
  die "下载的 reasonix-wrapper 格式异常"
grep -q '^# Invoke the real Termux Reasonix binary without injecting a permission mode\.$' "$WORK/reasonix-raw" || \
  die "下载的 reasonix-raw 格式异常"
bash -n "$WORK/reasonix-wrapper"
bash -n "$WORK/reasonix-raw"

backup_unmanaged() {
  local path="$1" backup stamp n
  [ -e "$path" ] || return 0
  is_managed "$path" && return 0
  stamp="$(date +%Y%m%d-%H%M%S)"
  backup="$path.backup-$stamp"
  n=1
  while [ -e "$backup" ]; do
    backup="$path.backup-$stamp-$n"
    n=$((n + 1))
  done
  mv "$path" "$backup"
  log "原文件不是本安装器管理的 wrapper，已备份到 $backup"
}

install_atomic() {
  local source="$1" target="$2" temp
  mkdir -p "$(dirname "$target")"
  backup_unmanaged "$target"
  temp="$(dirname "$target")/.${target##*/}.new.$$"
  install -m 755 "$source" "$temp"
  mv -f "$temp" "$target"
  log "已安装 $target"
}

# Safe aliases live beside the real binary and are immediately on Termux PATH.
install_atomic "$WORK/reasonix-wrapper" "$HELPER_DIR/reasonix-full"
install_atomic "$WORK/reasonix-raw" "$HELPER_DIR/reasonix-raw"

if [ "$MODE" = "default-full" ]; then
  install_atomic "$WORK/reasonix-wrapper" "$HOME_BIN/reasonix"

  case ":$PATH:" in
    *":$HOME_BIN:"*) ;;
    *)
      shell_name="${SHELL##*/}"
      case "$shell_name" in
        zsh) rc="$HOME/.zshrc" ;;
        bash) rc="$HOME/.bashrc" ;;
        *) rc="$HOME/.profile" ;;
      esac
      marker='# Reasonix Termux wrapper PATH'
      if ! grep -Fq "$marker" "$rc" 2>/dev/null; then
        printf '\n%s\nexport PATH="$HOME/bin:$PATH"\n' "$marker" >> "$rc"
        log "已将 ~/bin PATH 写入 $rc（重新打开终端后生效）"
      fi
      ;;
  esac

  cat <<'EOF'

已启用默认 Full access：新终端中的 `reasonix` 会注入 danger-full-access。
当前终端若尚未找到 ~/bin/reasonix，请执行：
  export PATH="$HOME/bin:$PATH"; hash -r

恢复原始权限行为：
  reasonix-raw ...
  REASONIX_FULL_ACCESS=0 reasonix ...
EOF
else
  cat <<'EOF'

已完成安全默认安装（没有覆盖 `reasonix`）：
  reasonix-full ...  # 默认 danger-full-access
  reasonix-raw ...   # 不注入权限模式

若明确希望 `reasonix` 本身默认 Full access，请重新运行一键命令并加：
  --default-full
EOF
fi

log "警告: danger-full-access 关闭 Reasonix OS 沙箱；只用于可信任务。"
