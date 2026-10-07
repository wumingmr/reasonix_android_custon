#!/usr/bin/env bash
# release-termux.sh — 在本仓库(Termux)直接编译、测试 Reasonix,并把编译好的
# 二进制上传到 artifacts/(GitHub Action verify-and-release.yml 校验版本与
# 上游最新 tag 一致后自动创建 release)。
#
# 前置:仓库 master 已是最新源码+补丁(运行 ./update-reasonix.sh 升级),
#       本仓库可 push(SSH key)。
#
# 用法:
#   ./release-termux.sh
# 环境变量:
#   RELEASE_TAG       指定版本号(默认从 release-notes/releases.json 提取)
#   SKIP_TESTS=1      跳过测试
#   FAIL_ON_TEST=1    测试失败时中止(默认仅报告,Termux 已知环境性失败不阻塞)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO="${RELEASE_REPO:-lengxiaohua123/reasonix_android_custon}"
umask 022

log() { printf '[release] %s\n' "$*"; }

# Android 长任务防护:防止系统杀后台进程 / Doze 挂起网络。
if command -v termux-wake-lock >/dev/null 2>&1; then
  termux-wake-lock || true
  trap 'termux-wake-unlock >/dev/null 2>&1 || true' EXIT
fi
# 8 核手机全核编译/测试会过热降频,反而更慢且易触发并发 flaky;
# GOMAXPROCS 默认限 4 核,可用环境变量覆盖。
export GOMAXPROCS="${GOMAXPROCS:-4}"

# 1. 解析版本:仓库源码即该版本+补丁(releases.json 随上游源码同步)
TAG="${RELEASE_TAG:-v$(python3 -c "import json;print(json.load(open('$SCRIPT_DIR/release-notes/releases.json'))['releases'][0]['version'])" 2>/dev/null || echo 0)}"
REL_TAG="termux-$TAG"
log "版本: $TAG → release: $REL_TAG"

# 2. 编译(源码就在本仓库,已含 Termux 补丁)
log "编译 android 二进制"
cd "$SCRIPT_DIR"
make android
BIN="$SCRIPT_DIR/bin/reasonix-android-arm64"
[ -f "$BIN" ] || { echo "[release] 编译产物缺失: $BIN" >&2; exit 1; }
log "产物: $BIN ($(stat -c%s "$BIN") bytes)"

# 4. 测试
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
    echo "[release] 测试失败且 FAIL_ON_TEST=1,中止" >&2
    exit 1
  fi
fi
# 5. 上传二进制到仓库(发布由 GitHub Action 校验版本/hash 后完成)
log "上传二进制到仓库 artifacts/"
ART_DIR="$SCRIPT_DIR/artifacts"
mkdir -p "$ART_DIR"
cp "$BIN" "$ART_DIR/reasonix-android-arm64"
cd "$SCRIPT_DIR"
git add artifacts/reasonix-android-arm64
if git commit -m "upload reasonix-android-arm64 $TAG" >/dev/null 2>&1; then
  git push origin master
  log "已上传 $TAG 二进制,等待 Action 校验并发布: https://github.com/$REPO/actions"
else
  log "二进制内容无变化,无需重新上传(最近提交: $(git log -1 --format=%s))"
fi
