#!/usr/bin/env bash
# rebuild-termux-patch.sh — 从当前上游版本到 HEAD 重建累积 Termux 补丁。
#
# reasonix-termux.patch 是 update-reasonix.sh 与 GitHub 同步 workflow 共同使用的
# 唯一源码补丁。每次提交 Android/Termux 专属源码改动后都必须重建并提交它。
#
# 用法:
#   ./rebuild-termux-patch.sh          # 重建并写入 reasonix-termux.patch
#   ./rebuild-termux-patch.sh --check  # 仅校验现有 patch 与 HEAD 是否一致
#
# 环境变量:
#   REASONIX_TAG         补丁基线 tag；默认从 release-notes/releases.json 读取
#   REASONIX_REPO_URL    上游仓库 URL
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_URL="${REASONIX_REPO_URL:-https://github.com/esengine/DeepSeek-Reasonix.git}"
PATCH_FILE="$SCRIPT_DIR/reasonix-termux.patch"
MODE="${1:-}"
[ -z "$MODE" ] || [ "$MODE" = "--check" ] || {
  echo "用法: $0 [--check]" >&2
  exit 2
}

log() { printf '[patch] %s\n' "$*"; }
die() { printf '[patch] 错误: %s\n' "$*" >&2; exit 1; }

cd "$SCRIPT_DIR"
git rev-parse --git-dir >/dev/null 2>&1 || die "必须在 git 仓库内运行"

# 补丁只从已提交的 HEAD 生成，避免工作区里半完成或未跟踪的文件被遗漏。
# reasonix-termux.patch 自身可以是旧的/脏的，因为本脚本正要替换它。
STATUS="$(git status --porcelain --untracked-files=all)"
OTHER_STATUS="$(printf '%s\n' "$STATUS" | grep -vE '^.. reasonix-termux\.patch$' || true)"
if [ -n "$OTHER_STATUS" ]; then
  printf '%s\n' "$OTHER_STATUS" >&2
  die "除 reasonix-termux.patch 外还有未提交修改；先提交源码/脚本，再重建补丁"
fi

TAG="${REASONIX_TAG:-v$(python3 -c "import json;print(json.load(open('$SCRIPT_DIR/release-notes/releases.json'))['releases'][0]['version'])" 2>/dev/null || true)}"
[ "$TAG" != "v" ] || die "无法从 release-notes/releases.json 解析上游版本"
case "$TAG" in
  v1.*) ;;
  *) die "补丁基线 $TAG 不在受支持的 1.x 线" ;;
esac

if ! git rev-parse --verify -q "$TAG^{commit}" >/dev/null; then
  log "本地没有 $TAG，正在从上游获取"
  git fetch --depth=1 "$REPO_URL" "tag" "$TAG"
fi

# 仓库基础设施不进入源码 patch：同步脚本在替换上游树时显式保留这些文件。
# 其余文件（包括定制 docs 与测试）都属于应随升级重放的源码改动。
PATHS=(
  .
  ':(exclude).github'
  ':(exclude).gitignore'
  ':(exclude).gitattributes'
  ':(exclude)artifacts'
  ':(exclude)bin'
  ':(exclude)reasonix-termux.patch'
  ':(exclude)rebuild-termux-patch.sh'
  ':(exclude)release-termux.sh'
  ':(exclude)release-notes-termux.md'
  ':(exclude)update-reasonix.sh'
)

TMP_ROOT="${REASONIX_TMPDIR:-${HOME}/tmp}"
mkdir -p "$TMP_ROOT"
WORK="$(mktemp -d "$TMP_ROOT/reasonix-patch-XXXXXX")"
GENERATED="$WORK/reasonix-termux.patch"
VERIFY="$WORK/base"
cleanup() {
  git worktree remove --force "$VERIFY" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

git diff --binary --full-index "$TAG" HEAD -- "${PATHS[@]}" > "$GENERATED"
[ -s "$GENERATED" ] || die "生成的补丁为空（$TAG 与 HEAD 没有定制源码差异）"

FILES="$(grep -c '^diff --git ' "$GENERATED" || true)"
for required in \
  internal/notify/sender_android.go \
  internal/cli/select.go \
  internal/session/history_locator.go \
  internal/config/render.go; do
  grep -q "^diff --git a/$required b/$required$" "$GENERATED" || \
    die "生成补丁漏了关键文件:$required"
done

# 在干净上游 worktree 上验证，防止生成或路径排除规则产出不可应用的 patch。
git worktree add --detach -q "$VERIFY" "$TAG"
(
  cd "$VERIFY"
  git apply --check "$GENERATED"
)

if [ "$MODE" = "--check" ]; then
  [ -f "$PATCH_FILE" ] || die "缺少 $PATCH_FILE"
  cmp -s "$GENERATED" "$PATCH_FILE" || \
    die "reasonix-termux.patch 已过期；运行 ./rebuild-termux-patch.sh 并提交结果"
  log "补丁与 $TAG..HEAD 一致，且可干净应用（$FILES files）"
  exit 0
fi

mv -f "$GENERATED" "$PATCH_FILE"
chmod 600 "$PATCH_FILE"
log "已重建 $PATCH_FILE：$TAG..HEAD，共 $FILES 个文件"
log "提交前请检查: git diff --stat -- reasonix-termux.patch"
