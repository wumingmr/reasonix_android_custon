#!/usr/bin/env bash
# update-reasonix.sh — 升级本仓库源码到上游最新 tag,并用 cherry-pick 应用
# termux-patch 分支上的 Termux 适配补丁,最后编译验证。
#
# 不再使用独立源码目录(reasonix-src):源码直接更新到本仓库 master。
#
# 用法:
#   ./update-reasonix.sh
# 环境变量:
#   REASONIX_REPO_URL   上游仓库(默认 https://github.com/esengine/DeepSeek-Reasonix.git)
#   REASONIX_TAG        指定 tag(默认取最新正式版 tag)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_URL="${REASONIX_REPO_URL:-https://github.com/esengine/DeepSeek-Reasonix.git}"
PATCH_BRANCH="termux-patch"
umask 022

log() { printf '[update] %s\n' "$*"; }

cd "$SCRIPT_DIR"
[ -d .git ] || { echo "[update] 错误: 必须在仓库内运行" >&2; exit 1; }

# 1. 解析上游最新 tag
TAG="${REASONIX_TAG:-$(git ls-remote --tags --refs "$REPO_URL" \
    | awk -F/ '{print $NF}' | grep -E '^v[0-9]+\.' | grep -v -- '-rc' | sort -V | tail -1)}"
[ -n "$TAG" ] || { echo "[update] 无法解析上游 tag" >&2; exit 1; }
log "上游最新 tag: $TAG"

# 当前源码版本(releases.json 随上游源码同步)
CUR="$(python3 -c "import json;print(json.load(open('$SCRIPT_DIR/release-notes/releases.json'))['releases'][0]['version'])" 2>/dev/null || echo unknown)"
log "当前源码版本: $CUR"
if [ "$CUR" = "${TAG#v}" ]; then
  log "已是最新,无需更新"
  exit 0
fi

# 2. fetch 上游 tag(对象用于 checkout 源码;浅获取即可)
git fetch --depth=1 "$REPO_URL" tag "$TAG" || { echo "[update] fetch $TAG 失败" >&2; exit 1; }

# 3. 备份专属文件(.gitignore/.gitattributes 含 LFS 配置)
cp .gitignore "$SCRIPT_DIR/.gitignore.bak"
cp .gitattributes "$SCRIPT_DIR/.gitattributes.bak"

# 4. 清空旧源码(保留 .git/.github/.reasonix/artifacts/脚本/patch/日志)
find . -mindepth 1 -maxdepth 1 ! -name .git ! -name .github ! -name .reasonix \
  ! -name artifacts ! -name reasonix-termux.patch ! -name update-reasonix.sh \
  ! -name release-termux.sh ! -name action.log ! -name '*.bak' -exec rm -rf {} +

# 5. 填入上游源码(排除 .github 与专属文件,稍后恢复)
git checkout "$TAG" -- . ':(exclude).github' || true
mv .gitignore.bak .gitignore
mv .gitattributes.bak .gitattributes

# 6. 提交上游源码快照
git add -A
if git diff --cached --quiet; then
  log "上游源码无变化(异常,请检查)"
else
  git commit -m "sync upstream $TAG source" >/dev/null
  log "已提交上游 $TAG 源码快照"
fi

# 7. cherry-pick Termux 补丁(termux-patch 分支 HEAD 为补丁 commit)
# 本地分支可能不存在(例如新 clone 或 fetch 后未建分支),此时从 origin 创建跟踪分支。
if ! git rev-parse --verify -q "$PATCH_BRANCH" >/dev/null; then
  log "本地分支 $PATCH_BRANCH 不存在,从 origin/$PATCH_BRANCH 创建"
  git branch --track "$PATCH_BRANCH" "origin/$PATCH_BRANCH" || {
    echo "[update] 无法创建本地分支 $PATCH_BRANCH(origin 无此分支?)" >&2
    exit 1
  }
fi
if git cherry-pick "$PATCH_BRANCH" 2>/dev/null; then
  log "补丁干净应用 ✓"
else
  if git status --porcelain | grep -qE '^(UU|AA|DD|AU|UA|DU|UD)'; then
    echo "[update] 补丁与 $TAG 冲突,请手工解决后 git cherry-pick --continue" >&2
    echo "[update] 若补丁中某些文件在 $TAG 已不需要,可用 git cherry-pick --skip" >&2
  else
    echo "[update] cherry-pick 失败(非冲突),请检查上方 git 报错" >&2
  fi
  exit 1
fi

# 8. 编译验证
log "编译验证..."
make android VERSION="$TAG"
log "完成: 源码=$TAG + 补丁 → bin/reasonix-android-arm64"
