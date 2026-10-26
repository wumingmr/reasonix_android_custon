# 版本号优先用显式传入的 VERSION(命令行 `make android VERSION=...` 会覆盖)。
#
# 不要用 `git describe --tags`:上游 tag(v1.39.8 等)在本仓库不可达,因为
# update-reasonix.sh 只做 `git checkout <tag> -- .`(只取文件内容,从不把上游
# commit 拉进本仓库历史)。describe 于是退回到 Termux 打包时留下的
# termux-v1.25.0 tag,算出 termux-v1.25.0-9-gXXXX 这类看着像版本大倒退的
# 字符串。真实基线以 release-notes/releases.json 为准(与该脚本一致)。
#
# 形如 v1.39.8-termux.4+input+cap.cfeb8193:上游版本 + Termux 定制标记 + 短 sha。
UPSTREAM_VERSION := $(shell python3 -c "import json;print(json.load(open('release-notes/releases.json'))['releases'][0]['version'])" 2>/dev/null)
ifeq ($(strip $(UPSTREAM_VERSION)),)
  # 无 releases.json(非官方源码树)时退回 describe,但只认 termux-v* 避免撞上上游 tag。
  UPSTREAM_VERSION := $(shell git describe --tags --match 'termux-v*' --always 2>/dev/null || echo dev)
endif
TERMUX_REVISION := $(shell git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)

VERSION ?= $(UPSTREAM_VERSION)-termux.$(TERMUX_REVISION)
BUILD_TIME_UTC := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GIT_COMMIT := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.gitCommit=$(GIT_COMMIT) \
	-X main.buildTimeUTC=$(BUILD_TIME_UTC)
GOEXE := $(shell go env GOEXE)
ANDROID_ARCH ?= arm64

# One pin for the Makefile and the CI lint job; see .github/workflows/ci.yml.
GOLANGCI_VERSION := $(shell cat .golangci-version)

.PHONY: build android vet fmt lint lint-go lint-install lint-cross lint-update test desktop-test desktop-test-short desktop-test-times sdk-test sdk-test-race hooks cross clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/reasonix$(GOEXE) ./cmd/reasonix
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/reasonix-plugin-example$(GOEXE) ./cmd/reasonix-plugin-example

android:
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=android GOARCH=$(ANDROID_ARCH) go build -ldflags "$(LDFLAGS)" -o bin/reasonix-android-$(ANDROID_ARCH) ./cmd/reasonix

vet:
	go vet ./...

fmt:
	gofmt -w .

# Both gates CI runs, at the version CI pins. Skipping golangci-lint locally
# trades a second here for a ten-minute CI round trip: `modernize` findings in
# particular never surface in `go vet`.
lint: lint-go
	go run ./tools/repolint

lint-go:
	@command -v golangci-lint >/dev/null || { echo "golangci-lint not installed; run: make lint-install"; exit 1; }
	@have=$$(golangci-lint version --short 2>/dev/null); want=$$(echo "$(GOLANGCI_VERSION)" | sed 's/^v//'); \
		[ "$$have" = "$$want" ] || echo "warning: local golangci-lint $$have, CI pins $$want (make lint-install)"
	golangci-lint run --timeout=5m ./...

# CGO_ENABLED=0 keeps the install working where a stray clang on PATH shadows
# the toolchain and breaks runtime/cgo.
lint-install:
	CGO_ENABLED=0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

lint-update:
	go run ./tools/repolint -update

# Linting one GOOS leaves every //go:build windows and darwin file unchecked.
lint-cross:
	@for t in "linux ." "darwin ." "windows ." "linux desktop" "windows desktop"; do \
		set -- $$t; \
		echo "== golangci-lint GOOS=$$1 ($$2)"; \
		(cd $$2 && GOOS=$$1 golangci-lint run --timeout=5m ./...) || exit 1; \
	done

test:
	go test ./...

# The desktop suite needs ~25m locally, past go's default 10m test alarm
# (the TaggedHistory migrations alone are ~389s), so these targets pin a wider one.
desktop-test:
	cd desktop && go test -timeout=25m .

desktop-test-short:
	cd desktop && go test -short -timeout=25m .

desktop-test-times:
	cd desktop && go test -count=1 -timeout=25m -json . | python3 ../scripts/desktop-test-times.py

sdk-test:
	cd sdk/go && go test ./...

sdk-test-race:
	cd sdk/go && go test -race ./...

hooks:
	@git config core.hooksPath .githooks
	@echo "installed: core.hooksPath -> .githooks (pre-push runs go vet)"

cross:
	@mkdir -p dist
	@for p in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o dist/reasonix-$$os-$$arch$$ext ./cmd/reasonix; \
	done

clean:
	rm -rf bin dist
