GO ?= go
BUILD_DIR ?= build
TARGET_GOOS := $(shell $(GO) env GOOS)
EXE := $(if $(filter windows,$(TARGET_GOOS)),.exe,)
BINARY ?= $(BUILD_DIR)/dora$(EXE)
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
RELEASE_LDFLAGS ?= -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)

.PHONY: build release test check

build: | $(BUILD_DIR)/
	$(GO) build -o $(BINARY) ./cmd/dora

release: | $(BUILD_DIR)/
	$(GO) build -trimpath -ldflags="$(RELEASE_LDFLAGS)" -o $(BINARY) ./cmd/dora

test:
	$(GO) test ./...

check: test
	$(GO) vet ./...
	$(GO) test -race ./...
	git diff --check

$(BUILD_DIR)/:
	mkdir -p $(BUILD_DIR)
