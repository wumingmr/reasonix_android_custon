#!/usr/bin/env bash
# release-termux.sh — 在本仓库(Termux)直接编译、测试 Reasonix,并把编译好的
# 二进制上传到 artifacts/(GitHub Action verify-and-release.yml 校验版本与
# 上游最新 tag 一致后自动创建 release)。
#
# 前置:仓库 master 已是最新源码+补丁(运行 ./update-reasonix.sh 升级),
#       本仓库可 push。
#
# 用法:
#   ./release-termux.sh
# 环境变量:
#   RELEASE_TAG       指定上游版本号(默认从 release-notes/releases.json 提取)。
#                     必须是 1.x 的 tag:2.x 重构了 internal/,Termux 补丁
#                     无法套用,详见 update-reasonix.sh 的 ALLOW_MAJOR 说明。
#   RELEASE_REPO      仅用于打印提示链接(实际推送目标是 git remote origin)。
#                     默认从 origin 自动推导,不再硬编码别人的仓库。
#   ANDROID_ARCH      目标架构(默认 arm64)
#   SKIP_TESTS=1      跳过测试
#   FAIL_ON_TEST=1    测试失败时中止(默认仅报告,Termux 已知环境性失败不阻塞)
#   ALLOW_DIRTY=1     允许在有未提交改动时继续(默认拒绝,避免把无关改动一起
#                     commit 进发布提交)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
umask 022

log() { printf '[release] %s\n' "$*"; }
die() { printf '[release] 错误: %s\n' "$*" >&2; exit 1; }

# Android 长任务防护:防止系统杀后台进程 / Doze 挂起网络。
# 必须在所有其它 trap 之前设置,后续代码不要覆盖这个 EXIT trap。
if command -v termux-wake-lock >/dev/null 2>&1; then
  termux-wake-lock || true
  trap 'termux-wake-unlock >/dev/null 2>&1 || true' EXIT
fi
# 8 核手机全核编译/测试会过热降频,反而更慢且易触发并发 flaky;
# GOMAXPROCS 默认限 4 核,可用环境变量覆盖。
export GOMAXPROCS="${GOMAXPROCS:-4}"

cd "$SCRIPT_DIR"
git rev-parse --git-dir >/dev/null 2>&1 || die "必须在 git 仓库内运行"

# 发布目标仓库从 origin 推导,不再硬编码。原默认值
# lengxiaohua123/reasonix_android_custon 是别人的仓库,推错会白跑一次编译,
# 而且 LFS 对象可能已经写进对方的仓库历史。
ORIGIN_URL="$(git remote get-url origin 2>/dev/null || echo '')"
[ -n "$ORIGIN_URL" ] || die "没有 origin remote,无法确定推送目标"
REPO="${RELEASE_REPO:-$ORIGIN_URL}"
REPO="${REPO%.git}"
REPO="${REPO#https://github.com/}"
REPO="${REPO#git@github.com:}"
REPO="${REPO#ssh://git@github.com/}"
log "推送目标: $REPO"

# git-lfs 是必需的:artifacts/* 走 LFS(.gitattributes),缺了它会把 54MB 的
# 二进制当普通文本塞进 git 对象库,仓库永久膨胀且不可逆。
git lfs version >/dev/null 2>&1 || die "需要 git-lfs(artifacts/ 依赖它);先安装并 git lfs install"
git lfs install --local >/dev/null 2>&1 || true

# 工作区必须干净:第 4 步 git add 虽只加 artifacts/ 一个文件,但 git commit
# 会把**所有**已暂存内容一起提交。若之前有别的改动被 add 过,它们会被混进
# 发布提交。默认拒绝,除非显式 ALLOW_DIRTY=1。
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  [ "${ALLOW_DIRTY:-0}" = "1" ] || die "工作区有未提交改动,拒绝发布。先提交或暂存,或设 ALLOW_DIRTY=1。"
  log "警告:ALLOW_DIRTY=1,带着未提交改动继续"
fi

# 1. 解析版本:仓库源码即该版本+补丁(releases.json 随上游源码同步)
TAG="${RELEASE_TAG:-v$(python3 -c "import json;print(json.load(open('$SCRIPT_DIR/release-notes/releases.json'))['releases'][0]['version'])" 2>/dev/null || echo 0)}"
case "$TAG" in
  v1.*) ;;
  *)
    printf '[release] 错误:版本 %s 不在 1.x 线内。\n' "$TAG" >&2
    printf '  2.x 重构了 internal/ 目录结构,本仓库的 Termux 适配无法套用;\n' >&2
    printf '  发布 2.x 二进制没有意义。\n' >&2
    exit 2
    ;;
esac
REL_TAG="termux-$TAG"
log "版本: $TAG → release tag: $REL_TAG"

# 2. 编译(源码就在本仓库,已含 Termux 补丁)
log "编译 android 二进制"
ANDROID_ARCH="${ANDROID_ARCH:-arm64}"
make android ANDROID_ARCH="$ANDROID_ARCH"
BIN="$SCRIPT_DIR/bin/reasonix-android-$ANDROID_ARCH"
[ -f "$BIN" ] || die "编译产物缺失: $BIN"
BIN_SIZE="$(stat -c%s "$BIN")"
BIN_HASH="$(sha256sum "$BIN" | cut -d' ' -f1)"
BIN_VERSION="$(go version -m "$BIN" 2>/dev/null | grep -oE 'main\.version=[^ ]+' | cut -d= -f2 || true)"
log "产物: $BIN ($BIN_SIZE bytes)"
log "sha256: $BIN_HASH"
log "内嵌版本: ${BIN_VERSION:-未知}"
if [ "${BIN_VERSION:-}" != "$TAG" ]; then
  log "提示:内嵌版本带 -termux.<sha> 后缀,与上游 tag $TAG 不相等。"
  log "      verify-and-release.yml 要求完全相等才发布,因此该 Action 会 skip。"
  log "      如需自动发布,需调整 Action 的比较逻辑或改用裸版本号构建。"
fi

# 3. 测试
if [ "${SKIP_TESTS:-0}" != "1" ]; then
  log "运行测试(日志: $SCRIPT_DIR/test-result.log)"
  set +e
  go test -count=1 ./... 2>&1 | tee "$SCRIPT_DIR/test-result.log"
  TEST_EXIT=${PIPESTATUS[0]}
  set -e
  # 全量并行下 Termux 资源有限,已知测试会偶发超时/排序 flaky;
  # 对失败包单独重跑一次,仍失败才算真失败。
  if [ "$TEST_EXIT" -ne 0 ]; then
    FAIL_PKGS="$(grep -E '^FAIL\s' "$SCRIPT_DIR/test-result.log" | awk '{print $2}' | sort -u)"
    RETEST_OK=1
    for p in $FAIL_PKGS; do
      if go test -count=1 "$p" >/dev/null 2>&1; then
        log "重跑通过(并发 flaky): $p"
      else
        log "重跑仍失败: $p"
        RETEST_OK=0
      fi
    done
    if [ "$RETEST_OK" = "1" ]; then
      log "所有失败均为并发 flaky,单独重跑全部通过"
      TEST_EXIT=0
    fi
  fi
  PASS="$(grep -cE '^ok\s' "$SCRIPT_DIR/test-result.log" || true)"
  FAIL="$(grep -cE '^--- FAIL' "$SCRIPT_DIR/test-result.log" || true)"
  log "测试结果: $PASS 包通过, $FAIL 处失败(go test 退出码 $TEST_EXIT)"
  if [ "${FAIL_ON_TEST:-0}" = "1" ] && [ "$TEST_EXIT" -ne 0 ]; then
    die "测试失败且 FAIL_ON_TEST=1,中止"
  fi
else
  log "SKIP_TESTS=1:跳过测试"
fi

# 4. 上传二进制到仓库(发布由 GitHub Action 校验版本/hash 后完成)
log "上传二进制到仓库 artifacts/"
ART_DIR="$SCRIPT_DIR/artifacts"
mkdir -p "$ART_DIR"
cp "$BIN" "$ART_DIR/reasonix-android-$ANDROID_ARCH"

# LFS 洁净过滤:暂存区里应只剩约 133 字节指针。若这里仍是 54MB 实体,说明
# LFS 规则没生效,提交进去会让仓库永久膨胀 —— 必须中止并撤销暂存。
git add "artifacts/reasonix-android-$ANDROID_ARCH"
STAGED_SIZE="$(git cat-file -s "$(git rev-parse ":artifacts/reasonix-android-$ANDROID_ARCH")")"
if [ "$STAGED_SIZE" -gt 4096 ]; then
  git reset -q HEAD -- "artifacts/reasonix-android-$ANDROID_ARCH" 2>/dev/null || true
  die "暂存区里是 $STAGED_SIZE 字节的实体文件,LFS 洁净过滤未生效。已撤销暂存,拒绝提交。检查 .gitattributes 与 git lfs install。"
fi
log "暂存区为 LFS 指针($STAGED_SIZE 字节),符合预期"

if git diff --cached --quiet; then
  log "二进制内容无变化,无需重新上传(最近提交: $(git log -1 --format=%s))"
else
  git commit -q -m "upload reasonix-android-$ANDROID_ARCH $TAG" || die "git commit 失败"
  # push 失败不回滚本地提交:二进制已就绪,修好网络后 git push 即可。
  # 但必须让失败可见 —— die 保证 set -e 下不会静默继续。
  git push origin master || die "git push 失败。提交已在本地,修好网络后 git push origin master 重试。"
  log "已上传 $TAG 二进制,等待 Action 校验并发布: https://github.com/$REPO/actions"
fi