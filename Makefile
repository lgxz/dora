GO ?= go
BUILD_DIR ?= build
TARGET_GOOS := $(shell $(GO) env GOOS)
EXE := $(if $(filter windows,$(TARGET_GOOS)),.exe,)
BINARY ?= $(BUILD_DIR)/dora$(EXE)
RELEASE_LDFLAGS ?= -s -w

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
