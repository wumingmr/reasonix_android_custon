#!/usr/bin/env bash
# update-reasonix.sh — 升级本仓库源码到上游最新 tag,并重新应用 Termux 适配
# 补丁,最后编译验证。
#
# 不再使用独立源码目录(reasonix-src):源码直接更新到本仓库 master。
#
# 补丁来源是本仓库 master 上最后一个 termux 适配 commit(见 TERMUX_PATCH_REF),
# 而不是 termux-patch 分支 —— 该分支基线早已停留在 v1.25.0,cherry-pick 到任何
# 更新的 tag 都会冲突,而 Termux 适配是跟着 master 一起手工演进到当前版本的。
#
# 用法:
#   ./update-reasonix.sh
# 环境变量:
#   REASONIX_REPO_URL   上游仓库(默认 https://github.com/esengine/DeepSeek-Reasonix.git)
#   REASONIX_TAG        指定 tag(默认取**最新 1.x** tag;1.x 之外的版本需配
#                       ALLOW_MAJOR=1 二次确认,见下)
#   ALLOW_MAJOR=1       允许同步到 1.x 之外的版本(2.x 目前无法套用 Termux 补丁)
#   TERMUX_PATCH_REF    Termux 适配 commit(默认自动探测 master 上最后一个改到全部
#                       适配文件(哨兵)的 termux: 提交;显式给值可手动覆盖,但
#                       非适配 commit 会被拒绝)
#   KEEP_CHANGES=1      失败时不回滚,保留现场供手工处理
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_URL="${REASONIX_REPO_URL:-https://github.com/esengine/DeepSeek-Reasonix.git}"
umask 022

log() { printf '[update] %s\n' "$*"; }
die() { printf '[update] 错误: %s\n' "$*" >&2; exit 1; }

cd "$SCRIPT_DIR"
[ -d .git ] || die "必须在仓库内运行"

# 0. 探测 Termux 适配 commit。master 上的形态是
#    <上游快照 commit> → <termux 适配 commit>,后者即补丁真身。
#
#    ⚠️ 不能只用 "最后一个 termux: 开头的 commit" 来定位补丁源。
#    e233da5(termux: 修正版本号推导)只改了 Makefile 和两个脚本,却是适配
#    commit cfeb819 之后最新的 termux: 提交,于是被 head -1 选中。实测后果:
#    cherry-pick 它会与上游 release-termux.sh 冲突;强行解冲突后,Android 通知、
#    选择器中文输入修复、resume_list_limit 全部消失(回到"能编译但中文打不
#    进去"),而第 9 步 make android 仍会通过 —— 因为 GOOS=android 满足 linux
#    build tag,sender_linux.go 会接手编译。所以编译验证挡不住这种静默降级,
#    只能在选源这一层挡。
#
#    判据:一个 commit 只有同时改到下面全部适配文件才算补丁真身。
#    这份哨兵列表与"平台适配 + 本地改动必须合并为单一 commit"的约定对应:
#    只改脚本的 termux: 提交不是适配源。
PATCH_SENTINELS="internal/notify/sender_android.go internal/cli/select.go internal/config/config.go"

# is_patch_source 报告 commit c 是否改到了全部哨兵文件。
is_patch_source() {
  local c="$1" f
  for f in $PATCH_SENTINELS; do
    [ -n "$(git show --format= --name-only --diff-filter=ADMRT "$c" -- "$f" 2>/dev/null)" ] || return 1
  done
  return 0
}

resolve_patch_ref() {
  if [ -n "${TERMUX_PATCH_REF:-}" ]; then
    git rev-parse --verify -q "$TERMUX_PATCH_REF^{commit}" >/dev/null || \
      die "TERMUX_PATCH_REF=$TERMUX_PATCH_REF 不是有效 commit"
    is_patch_source "$TERMUX_PATCH_REF" || \
      die "TERMUX_PATCH_REF=$TERMUX_PATCH_REF 不是 Termux 适配 commit:未同时改到($PATCH_SENTINELS)。
       用它当补丁源会在升级后丢掉 Android 支持。请改用真正的适配 commit。"
    printf '%s\n' "$TERMUX_PATCH_REF"
    return
  fi
  # 从新到旧逐个筛查,取第一个改到全部哨兵文件的 termux: commit。
  # 提示信息走 stderr:本函数的 stdout 会被下面的命令替换捕获成 PATCH_REF。
  local c
  while read -r c; do
    if is_patch_source "$c"; then
      printf '%s\n' "$c"
      return 0
    fi
    printf '[update] 跳过 %s %s\n' "$(git rev-parse --short "$c")" "$(git log -1 --format='%s' "$c")" >&2
    printf '        (未改到全部适配文件,不是补丁真身)\n' >&2
  done < <(git log --format='%H %s' master | grep -E '^[0-9a-f]+ termux:' | cut -d' ' -f1 || true)
  return 1
}

PATCH_REF="$(resolve_patch_ref)" || PATCH_REF=""
[ -n "$PATCH_REF" ] || die "未找到 Termux 适配 commit(需同时改到:$PATCH_SENTINELS);可用 TERMUX_PATCH_REF=<sha> 指定"
log "Termux 适配补丁: $(git log -1 --format='%h %s' "$PATCH_REF")"

# 回滚锚点:升级前的 HEAD 与分支,任何失败都整体恢复。
ORIG_HEAD_REF="$(git rev-parse HEAD)"
log "回滚锚点: $ORIG_HEAD_REF"

rollback() {
  rc=$?
  if [ "${KEEP_CHANGES:-0}" = "1" ]; then
    printf '[update] 失败(rc=%s),KEEP_CHANGES=1 保留现场\n' "$rc" >&2
  else
    printf '[update] 失败(rc=%s),回滚到 %s\n' "$rc" "$ORIG_HEAD_REF" >&2
    git cherry-pick --abort >/dev/null 2>&1 || true
    git cherry-pick --quit  >/dev/null 2>&1 || true
    git reset --hard "$ORIG_HEAD_REF" >/dev/null 2>&1 || true
    git clean -fdq -e '*.bak' >/dev/null 2>&1 || true
    # git clean 会连未跟踪的本地脚本一起删,而它们的备份在仓库外的
    # LOCAL_BACKUP 里,必须在此恢复,否则一次失败的升级就丢了本地文件。
    # LOCAL_BACKUP 到第 4 步才创建,此前任何失败也会走到这里,所以要容忍
    # 它尚未定义 —— set -u 下未定义变量会直接终止脚本。
    if [ -n "${LOCAL_BACKUP:-}" ] && [ -f "$LOCAL_BACKUP/untracked.tar" ]; then
      (cd "$SCRIPT_DIR" && tar -xf "$LOCAL_BACKUP/untracked.tar") >/dev/null 2>&1 || true
      log "已恢复 $(wc -l <"$LOCAL_BACKUP/untracked.txt" 2>/dev/null || echo 0) 个本地未跟踪文件"
    fi
    if [ -n "${LOCAL_BACKUP:-}" ]; then rm -rf "$LOCAL_BACKUP"; fi
    log "已回滚;KEEP_CHANGES=1 可保留现场手工处理"
  fi
  exit "$rc"
}
trap rollback EXIT

# 1. 解析上游最新 tag
#    注意 sort -V 会把 v2.x 排在 v1.x 之后,所以"上游最新 tag"默认就是 2.x。
#    但本仓库的 Termux 适配停在 1.x:2.x 重构了 internal/ 目录结构
#    (internal/cli -> internal/frontend/cli、module 改为 reasonix、Go 1.26),
#    Termux 补丁在其中 38 个文件全部无法应用,且 2.x 不含任何 Android 支持。
#    所以默认只认 1.x 线;越界必须由调用方显式指定并二次确认。
LATEST_1X="$(git ls-remote --tags --refs "$REPO_URL" \
    | awk -F/ '{print $NF}' | grep -E '^v1\.[0-9]+\.' | grep -v -- '-rc' | sort -V | tail -1)"
LATEST_ANY="$(git ls-remote --tags --refs "$REPO_URL" \
    | awk -F/ '{print $NF}' | grep -E '^v[0-9]+\.' | grep -v -- '-rc' | sort -V | tail -1)"

if [ -n "${REASONIX_TAG:-}" ]; then
  TAG="$REASONIX_TAG"
  # 显式指定也算一次选择,但目标在 1.x 线之外时仍要确认 —— REASONIX_TAG 可能
  # 来自 cron、CI 或 shell 历史,不能当作"人正在看着屏幕"。
  case "$TAG" in
    v1.*) ;;
    *)
      cat >&2 <<EOF
[update] 警告:目标 $TAG 在 1.x 线之外。

  2.x 起重构了 internal/ 目录结构,Termux 适配补丁无法直接应用,Android
  支持也不存在。预期这会失败并自动回滚。

  若确实要试,再次运行并设置 ALLOW_MAJOR=1:
      REASONIX_TAG=$TAG ALLOW_MAJOR=1 $0
EOF
      [ "${ALLOW_MAJOR:-0}" = "1" ] || { echo "[update] 已中止(未设 ALLOW_MAJOR=1)" >&2; exit 2; }
      log "ALLOW_MAJOR=1:继续,但预期补丁冲突或构建失败"
      ;;
  esac
else
  TAG="${LATEST_1X:-$LATEST_ANY}"
  if [ -z "$TAG" ]; then
    echo "[update] 无法解析上游 tag" >&2; exit 1
  fi
  if [ -n "$LATEST_ANY" ] && [ "$LATEST_ANY" != "$TAG" ]; then
    log "提示:上游还有更新的 $LATEST_ANY(2.x 线)。本仓库 Termux 适配停在 1.x,已自动选 $TAG。"
    log "      如需 1.x 之外的版本,见上方 ALLOW_MAJOR 说明。"
  fi
fi
[ -n "$TAG" ] || { echo "[update] 无法解析上游 tag" >&2; exit 1; }
log "上游目标 tag: $TAG"

# 当前源码版本(releases.json 随上游源码同步)
CUR="$(python3 -c "import json;print(json.load(open('$SCRIPT_DIR/release-notes/releases.json'))['releases'][0]['version'])" 2>/dev/null || echo unknown)"
log "当前源码版本: $CUR"
if [ "$CUR" = "${TAG#v}" ]; then
  log "已是最新,无需更新"
  trap - EXIT
  exit 0
fi

# 2. fetch 上游 tag(对象用于 checkout 源码;浅获取即可)
git fetch --depth=1 "$REPO_URL" tag "$TAG" || { echo "[update] fetch $TAG 失败" >&2; exit 1; }

# 3. 备份专属文件(.gitignore/.gitattributes 含 LFS 配置)
cp .gitignore "$SCRIPT_DIR/.gitignore.bak"
cp .gitattributes "$SCRIPT_DIR/.gitattributes.bak"

# 4. 清空旧源码。仓库自身文件、Termux 产物与脚本需要保留;bin/ 不在其中 ——
#    旧二进制会被第 9 步的新构建覆盖,留着只会混淆"已安装的是哪一版"。
#    scripts/ 里有上游自带的数百个文件,也有本地新增的工具脚本(如
#    merge-makefile-conflict.py);整目录删掉再 checkout 恢复会连带丢掉未跟踪
#    的本地脚本,所以先把未跟踪文件备份到仓库之外再删。备份必须放在仓库外:
#    回滚路径会跑 git clean,放在仓库内的备份会被连坐删除。
LOCAL_BACKUP="$(mktemp -d "${TMPDIR:-/tmp}/reasonix-update-XXXXXX")"
UNTRACKED_LIST="$LOCAL_BACKUP/untracked.txt"
git ls-files --others --exclude-standard -- scripts release-termux.sh >"$UNTRACKED_LIST" || true
if [ -s "$UNTRACKED_LIST" ]; then
  log "备份 $(wc -l <"$UNTRACKED_LIST") 个未跟踪本地文件"
  tar -cf "$LOCAL_BACKUP/untracked.tar" -T "$UNTRACKED_LIST"
fi

find . -mindepth 1 -maxdepth 1 ! -name .git ! -name .github ! -name .reasonix \
  ! -name artifacts ! -name reasonix-termux.patch \
  ! -name update-reasonix.sh \
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

rollback_hint() {
  if [ "${KEEP_CHANGES:-0}" = "1" ]; then
    printf '补丁与 %s 冲突;已保留现场,可 git cherry-pick --continue 继续' "$TAG"
  else
    printf '补丁与 %s 冲突;将自动回滚到升级前状态' "$TAG"
  fi
}

# 7. 重新应用 Termux 适配补丁。
#    补丁取自已解析出的 PATCH_REF(master 上最后一个 termux 适配 commit),
#    而不是 termux-patch 分支 —— 后者基线停留在 v1.25.0,cherry-pick 到任何更新
#    的 tag 都会大面积冲突。PATCH_REF 的适配是随 master 手工演进到当前版本的,
#    对更新的上游 tag 重放才是正确路径。
if git cherry-pick "$PATCH_REF" >/dev/null 2>&1; then
  log "补丁干净应用 ✓"
else
  if git status --porcelain | grep -qE '^(UU|AA|DD|AU|UA|DU|UD)'; then
    conflicted="$(git status --porcelain | grep -E '^(UU|AA|DD|AU|UA|DU|UD)' | awk '{print $NF}')"
    log "以下文件与 $TAG 冲突:"
    printf '  %s\n' $conflicted >&2
    die "$(rollback_hint)"
  fi
  die "cherry-pick 失败(非冲突);$(rollback_hint)"
fi

# 8. 恢复未跟踪的本地脚本
if [ -s "$LOCAL_BACKUP/untracked.tar" ]; then
  log "恢复本地未跟踪文件"
  tar -xf "$LOCAL_BACKUP/untracked.tar" || die "恢复本地脚本失败"
fi
rm -rf "$LOCAL_BACKUP"

# 9. 编译验证
#    不传 VERSION:Makefile 会用 release-notes/releases.json 里的真实上游版本
#    拼出 <upstream>-termux.<sha>。早先这里传的是裸 $TAG,装上去后
#    `reasonix --version` 显示 v1.39.8,与官方版无法区分;而且 Makefile 曾用
#    `git describe --tags`,在上游 tag 不可达的本仓库里算出 termux-v1.25.0-*。
log "编译验证...(版本号由 Makefile 推导)"
make android

log "完成: 源码=$TAG + 补丁($(git log -1 --format='%h' "$PATCH_REF")) → bin/reasonix-android-arm64"
trap - EXIT
