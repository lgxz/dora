GO ?= go
BUILD_DIR ?= build
RELEASE_DIR ?= dist
TARGET_GOOS := $(shell $(GO) env GOOS)
EXE := $(if $(filter windows,$(TARGET_GOOS)),.exe,)
BINARY ?= $(BUILD_DIR)/dora$(EXE)
RELEASE_BINARY ?= $(RELEASE_DIR)/dora$(EXE)
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
RELEASE_LDFLAGS ?= -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)
PREFIX ?= $(HOME)/.local

# Cross-compile a statically-linked Linux binary. GOARCH defaults to arm64
# (Apple Silicon host); override with GOARCH=amd64 for x86_64 targets.
GOOS ?= linux
GOARCH ?= arm64
CGO_ENABLED ?= 0
LINUX_BINARY ?= $(RELEASE_DIR)/dora-linux-$(GOARCH)

.PHONY: build release install test check release-linux

build: | $(BUILD_DIR)/
	$(GO) build -o $(BINARY) ./cmd/dora

release: | $(RELEASE_DIR)/
	$(GO) build -trimpath -ldflags="$(RELEASE_LDFLAGS)" -o $(RELEASE_BINARY) ./cmd/dora

install: release
	mkdir -p $(PREFIX)/bin
	install -m 0755 $(RELEASE_BINARY) $(PREFIX)/bin/dora

# Cross-compile a statically-linked Linux binary.
#   make release-linux                 # -> dist/dora-linux-arm64
#   make release-linux GOARCH=amd64    # -> dist/dora-linux-amd64
release-linux: | $(RELEASE_DIR)/
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=$(CGO_ENABLED) \
		$(GO) build -trimpath -ldflags="$(RELEASE_LDFLAGS)" -o $(LINUX_BINARY) ./cmd/dora

test:
	$(GO) test ./...

check: test
	$(GO) vet ./...
	$(GO) test -race ./...
	git diff --check

$(BUILD_DIR)/:
	mkdir -p $(BUILD_DIR)

$(RELEASE_DIR)/:
	mkdir -p $(RELEASE_DIR)
