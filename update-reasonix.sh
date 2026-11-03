#!/usr/bin/env bash
# update-reasonix.sh — 升级本仓库源码到上游最新 tag,并重新应用 Termux 适配
# 补丁,最后编译验证。
#
# 不再使用独立源码目录(reasonix-src):源码直接更新到本仓库 master。
#
# reasonix-termux.patch 是唯一补丁源:本地升级脚本与 GitHub 同步 workflow 都应用
# 它。每次本地定制源码提交后运行 ./rebuild-termux-patch.sh 重新生成,可避免过去
# "只 cherry-pick 一个旧适配 commit"导致后续独立修复在升级时静默丢失的问题。
#
# 用法:
#   ./update-reasonix.sh
#   ./update-reasonix.sh --verify-local  # 仅校验适配，不联网或替换源码
# 环境变量:
#   REASONIX_REPO_URL   上游仓库(默认 https://github.com/esengine/DeepSeek-Reasonix.git)
#   REASONIX_TAG        指定 tag(默认取**最新 1.x** tag;1.x 之外的版本需配
#                       ALLOW_MAJOR=1 二次确认,见下)
#   ALLOW_MAJOR=1       允许同步到 1.x 之外的版本(2.x 目前无法套用 Termux 补丁)
#   TERMUX_PATCH_FILE   累积补丁路径(默认 ./reasonix-termux.patch)
#   KEEP_CHANGES=1      失败时不回滚,保留现场供手工处理
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_URL="${REASONIX_REPO_URL:-https://github.com/esengine/DeepSeek-Reasonix.git}"
umask 022

log() { printf '[update] %s\n' "$*"; }
die() { printf '[update] 错误: %s\n' "$*" >&2; exit 1; }

cd "$SCRIPT_DIR"
git rev-parse --git-dir >/dev/null 2>&1 || die "必须在仓库内运行"

require_clean_worktree() {
  [ -z "$(git status --porcelain --untracked-files=all)" ] || \
    die "工作区有未提交或未跟踪文件，拒绝升级；请先提交或移到仓库外"
}
require_clean_worktree

# 0. 校验唯一补丁源。过去这里从 commit 历史里探测一个"适配 commit"并只
#    cherry-pick 它;后续独立提交的 TUI resume 优化和配置修复因此会在下一次
#    上游同步时被清空。现在本地脚本与 GitHub workflow 统一应用同一个累积 patch。
PATCH_FILE="${TERMUX_PATCH_FILE:-$SCRIPT_DIR/reasonix-termux.patch}"

verify_local_adaptations() {
[ -s "$PATCH_FILE" ] || die "累积补丁不存在或为空:$PATCH_FILE。先运行 ./rebuild-termux-patch.sh"
PATCH_FILES="$(grep -c '^diff --git ' "$PATCH_FILE" || true)"
[ "${PATCH_FILES:-0}" -gt 0 ] || die "累积补丁没有 diff:$PATCH_FILE"
for required in \
  internal/notify/sender_android.go \
  internal/cli/select.go \
  internal/session/history_locator.go \
  internal/config/render.go; do
  grep -q "^diff --git a/$required b/$required$" "$PATCH_FILE" || \
    die "累积补丁漏了关键文件:$required。先运行 ./rebuild-termux-patch.sh"
done
# 文件在补丁里 ≠ 修复内容还在。历史上一次人工解决 cherry-pick 冲突时,
# select.go 的方向键修复被静默丢掉,测试也随文件一起没了,丢失持续了四天、
# 发了两版 Release 才被用户踩到。所以除了"文件存在",还要校验"修复符号存在"。
# 这里看的是 HEAD 的源码,不是补丁文本:文件在补丁里只说明补丁覆盖过它。
for required in \
  "internal/cli/select.go:escArrow:选择器搜索方向键修复" \
  "internal/cli/select.go:escSeqLen:选择器转义序列识别" \
  "internal/config/model_window.go:EffectiveContextWindow:未声明窗口兜底" \
  "internal/config/model_limits_discovery.go:ApplyDiscoveredLimits:/models 容量回填" \
  "internal/fileutil/atomicwrite.go:publishNewFile:Android 原子创建" \
  "internal/cli/select.go:updateSearchText:中文跨读取 pending" \
  "internal/cli/branch.go:clipReplayHistoryWithOmitted:索引尾部省略计数" \
  "internal/session/history_locator.go:HistoryTailWithOmitted:历史尾部快照计数" \
  "internal/cli/setup_image_capabilities.go:setupModelItems:setup 图片能力提示" \
  "internal/cli/setup_image_capabilities.go:persistImageCatalogs:图片能力保存" \
  "internal/provider/image_capability.go:ResolveImageInputMetadata:未知图片能力保留" \
  "internal/agent/imageinput.go:newImageInput:Agent 图片 fallback 决策" \
  "internal/config/model_effort.go:SetModelEffort:每模型独立 effort" \
  "internal/config/model_reasoning_discovery.go:ApplyDiscoveredReasoning:setup 思考声明保存" \
  "internal/provider/openai/reasoning_metadata.go:parseReasoningMetadata:思考元数据解析"; do
  file="${required%%:*}"; rest="${required#*:}"
  sym="${rest%%:*}"; label="${rest#*:}"
  # 匹配定义而非任意引用:escArrow 主体被删但 searchText 里还留着一次调用时,
  # 子串匹配照样命中,校验就形同虚设。定义形式只有两种 —— 顶层 func/const/var,
  # 以及方法(func (recv ...) Name)。
  grep -qE "func ${sym}\(|func \([^)]*\) ${sym}\(|^(const|var) ${sym}\b" \
    "$SCRIPT_DIR/$file" 2>/dev/null || \
    die "本地修复丢失:$file 里找不到 $sym 的定义($label)。某次上游同步或人工修改
    把这段修复覆盖掉了。用 git log -S $sym -- $file 找回引入它的提交。"
done
# 测试是丢失发生时唯一没响的报警器,所以关键测试的存在本身也要校验。
for required in \
  "internal/cli/select_search_input_test.go:TestEscArrowClaimsArrowsBeforeBareEsc" \
  "internal/cli/select_search_input_test.go:TestSearchBranchChecksArrowsBeforeBareEsc" \
  "internal/config/model_window_test.go:TestResolveContextWindowNamesTheOrigin" \
  "internal/config/model_limits_test.go:TestApplyDiscoveredLimitsFillsOnlyGaps" \
  "internal/fileutil/atomiccreate_android_test.go:TestAtomicCreateAndroidNoReplaceRace" \
  "internal/fileutil/atomiccreate_android_test.go:TestAtomicCreateAndroidFailsClosedWithoutRename" \
  "internal/cli/select_search_input_test.go:TestUpdateSearchTextRetainsPendingAcrossReads" \
  "internal/cli/replay_tail_test.go:TestReplayIndexedTailPreservesOmittedAndTurnBoundary" \
  "internal/session/projection_read_audit_test.go:TestSessionReadPathsReuseIntegrityAndRecheckChanges" \
  "internal/cli/setup_image_capabilities_test.go:TestSetupImageCatalogSavesOnlyAfterCommit" \
  "internal/cli/setup_image_capabilities_test.go:TestSetupImageCatalogFetchUsesOnlyModelMetadata" \
  "internal/boot/unknown_image_metadata_test.go:TestLocalResolverKeepsUnknownImagesAvailableWithoutFallback" \
  "internal/provider/openai/image_test.go:TestUnknownImageMetadataSerializesOnlyAttachedImages" \
  "internal/boot/unknown_image_metadata_test.go:TestBuildUnknownImageAttachmentReachesProvider" \
  "internal/agent/imageinput_test.go:TestUnknownImageInputRespectsFallback" \
  "internal/config/model_effort_test.go:TestModelEffortIndependentRoundTripAndRuntimePriority" \
  "internal/cli/setup_reasoning_test.go:TestSetupReasoningSavesDeclarationsAndImagesTogether" \
  "internal/provider/openai/reasoning_metadata_test.go:TestReasoningMetadataOnlyTrustsDeclarations" \
  "internal/boot/rebuild_config_classification_test.go:TestRuntimeConfigClassificationCoversExportedFields"; do
  file="${required%%:*}"; test_name="${required#*:}"
  grep -q "func $test_name(" "$SCRIPT_DIR/$file" 2>/dev/null || \
    die "关键测试丢失:$file 里找不到 $test_name()。测试没了就没有报警器,
    用 git log -- $file 找回包含它的提交。"
done
for required in \
  'scripts/termux/reasonix-wrapper:    exec "$REAL" "$subcommand" --permission-mode:wrapper 子命令路由' \
  'scripts/termux/compat.test.mjs:update network failure leaves ignored data and HEAD untouched:升级回滚测试' \
  '.github/workflows/build_termux.yml:tar.gz/${{ github.sha }}:CI 固定源码提交' \
  '.github/workflows/verify-and-release.yml:--verify-tag:发版必须先创建 tag' \
  'release-termux.sh:[ "$BIN_BASE" = "${TAG#v}" ] ||:发版产物版本校验' \
  'scripts/termux/release-identity.sh:termux_release_identity():修订 tag 格式校验'; do
  file="${required%%:*}"; rest="${required#*:}"
  marker="${rest%%:*}"; label="${rest#*:}"
  grep -Fq -- "$marker" "$SCRIPT_DIR/$file" || die "本地适配丢失:$label ($file)"
done
log "Termux 累积补丁:$PATCH_FILE ($PATCH_FILES files)"
log "本地修复符号与关键测试校验通过"
}

verify_local_adaptations
if [ "${1:-}" = "--verify-local" ]; then exit 0; fi

ORIG_HEAD_REF="$(git rev-parse HEAD)"

rollback() {
  rc=$?
  if [ "${KEEP_CHANGES:-0}" = "1" ]; then
    printf '[update] 失败(rc=%s),KEEP_CHANGES=1 保留现场\n' "$rc" >&2
  else
    printf '[update] 失败(rc=%s),回滚到 %s\n' "$rc" "$ORIG_HEAD_REF" >&2
    # Only indexed source files were changed. Never clean unrelated files;
    # reset also removes newly indexed files introduced by checkout/apply.
    git reset --hard "$ORIG_HEAD_REF" || \
      die "自动回滚失败，请保留现场并手工恢复 $ORIG_HEAD_REF"
    log "已回滚;KEEP_CHANGES=1 可保留现场手工处理"
  fi
  exit "$rc"
}

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

# Network resolution/fetch failures must never trigger a destructive rollback.
# Recheck after fetch in case another process edited the checkout meanwhile.
require_clean_worktree
[ "$(git rev-parse HEAD)" = "$ORIG_HEAD_REF" ] || die "升级期间 HEAD 已变化，请重试"

# Replace only tracked source paths. Ignored output, local state and unrelated
# files survive both successful syncs and rollback; no whole-directory deletion.
SOURCE_PATHS=(
  . ':(exclude).github' ':(exclude).gitignore' ':(exclude).gitattributes'
  ':(exclude).reasonix' ':(exclude)artifacts' ':(exclude)bin'
  ':(exclude)reasonix-termux.patch' ':(exclude)update-reasonix.sh'
  ':(exclude)rebuild-termux-patch.sh' ':(exclude)release-termux.sh'
  ':(exclude)release-notes-termux.md'
)
trap rollback EXIT
log "回滚锚点: $ORIG_HEAD_REF"
git rm -r --quiet --ignore-unmatch -- "${SOURCE_PATHS[@]}"
git checkout "$TAG" -- "${SOURCE_PATHS[@]}"

# 6. checkout/rm 已把源码快照写入 index，不把同期出现的本地文件混入提交。
if git diff --cached --quiet; then
  log "上游源码无变化(异常,请检查)"
else
  git commit -m "sync upstream $TAG source" >/dev/null
  log "已提交上游 $TAG 源码快照"
fi

rollback_hint() {
  if [ "${KEEP_CHANGES:-0}" = "1" ]; then
    printf '补丁与 %s 冲突;已保留上游快照，请手工修正并重新应用 patch' "$TAG"
  else
    printf '补丁与 %s 冲突;将自动回滚到升级前状态' "$TAG"
  fi
}

# 7. 重新应用累积 Termux 补丁。先 --check,失败时不留下半应用状态;KEEP_CHANGES
#    只保留刚提交的上游快照,方便手工在其上解决 patch 冲突。
if git apply --check "$PATCH_FILE"; then
  git apply --index "$PATCH_FILE"
  git commit -m "termux: apply cumulative Android adaptation for $TAG" >/dev/null
  log "累积补丁干净应用 ✓ ($PATCH_FILES files)"
else
  die "$(rollback_hint)"
fi

# Recheck definitions/tests after replay, not just before deleting old source.
verify_local_adaptations

# 8. 上游基线已经变成 $TAG；立即重建累积 patch 并提交。否则仓库里的 patch
#    仍以旧 tag 为基线，release-termux.sh 的一致性检查会拒绝发版。
[ -x "$SCRIPT_DIR/rebuild-termux-patch.sh" ] || die "缺少可执行的 rebuild-termux-patch.sh"
"$SCRIPT_DIR/rebuild-termux-patch.sh"
git add reasonix-termux.patch
if ! git diff --cached --quiet -- reasonix-termux.patch; then
  git commit -m "chore(termux): rebuild cumulative patch for $TAG" >/dev/null
  log "已按 $TAG 重建并提交累积补丁"
fi

# 10. 编译验证
#    不传 VERSION:Makefile 会用 release-notes/releases.json 里的真实上游版本
#    拼出 <upstream>-termux.<sha>。早先这里传的是裸 $TAG,装上去后
#    `reasonix --version` 显示 v1.39.8,与官方版无法区分;而且 Makefile 曾用
#    `git describe --tags`,在上游 tag 不可达的本仓库里算出 termux-v1.25.0-*。
log "编译验证...(版本号由 Makefile 推导)"
make android

log "完成: 源码=$TAG + 累积补丁($PATCH_FILES files) → bin/reasonix-android-arm64"
trap - EXIT
