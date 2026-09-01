.DEFAULT_GOAL := help

GO ?= go
APP ?= ./cmd/compact-proxy
BUILD_DIR ?= build
ARGS ?=
COVERAGE_FILE ?= coverage.out
COVERAGE_HTML ?= coverage.html
LDFLAGS ?= -s -w
INSTALL_DIR ?= $(shell $(GO) env GOBIN)

ifeq ($(strip $(INSTALL_DIR)),)
INSTALL_DIR := $(shell $(GO) env GOPATH)/bin
endif

BINARY ?= $(BUILD_DIR)/cproxy

.PHONY: help all build build-release release install uninstall run serve login login-device auth-status logout setup-crush fmt fmt-check test test-race bench vet coverage coverage-html mod-tidy mod-download mod-verify check clean clean-cache version

help:
	@echo "Compact Proxy development targets:"
	@echo "  make build          Build a stripped binary at build/cproxy"
	@echo "  make build-release  Alias for make build"
	@echo "  make install        Install cproxy into Go's bin directory"
	@echo "  make uninstall      Remove the installed cproxy binary"
	@echo "  make run ARGS=...   Run the CLI with arguments"
	@echo "  make serve ARGS=... Start the proxy server"
	@echo "  make login          Run browser OAuth login"
	@echo "  make login-device   Run device-code OAuth login"
	@echo "  make auth-status    Show authentication status"
	@echo "  make logout         Revoke and remove the primary auth token"
	@echo "  make setup-crush    Print Crush provider setup"
	@echo "  make fmt            Format Go source"
	@echo "  make fmt-check      Fail if Go source is not formatted"
	@echo "  make test           Run the Go test suite"
	@echo "  make test-race      Run tests with the race detector"
	@echo "  make bench          Run Go benchmarks"
	@echo "  make vet            Run go vet"
	@echo "  make coverage       Write coverage.out"
	@echo "  make coverage-html  Write coverage.html"
	@echo "  make mod-tidy       Tidy go.mod and go.sum"
	@echo "  make mod-download   Download Go modules"
	@echo "  make mod-verify     Verify downloaded Go modules"
	@echo "  make check          Format-check, test, vet, and build"
	@echo "  make clean          Remove local build and coverage output"
	@echo "  make clean-cache    Clear the Go test cache"
	@echo "  make version        Print the installed Go version"

all: check

build: $(BUILD_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o "$(BINARY)" $(APP)

build-release: build

release: build-release

install:
	mkdir -p "$(INSTALL_DIR)"
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o "$(INSTALL_DIR)/cproxy" $(APP)

uninstall:
	rm -f "$(INSTALL_DIR)/cproxy"

run:
	$(GO) run $(APP) $(ARGS)

serve:
	$(GO) run $(APP) serve $(ARGS)

login:
	$(GO) run $(APP) login $(ARGS)

login-device:
	$(GO) run $(APP) login-device $(ARGS)

auth-status:
	$(GO) run $(APP) auth status $(ARGS)

logout:
	$(GO) run $(APP) logout $(ARGS)

setup-crush:
	$(GO) run $(APP) setup crush $(ARGS)

fmt:
	gofmt -w cmd internal

fmt-check:
	@test -z "$$(gofmt -l cmd internal)"

test:
	$(GO) test -count=1 ./...

test-race:
	$(GO) test -race ./...

bench:
	$(GO) test -bench=. ./...

vet:
	$(GO) vet ./...

coverage:
	$(GO) test -coverprofile="$(COVERAGE_FILE)" ./...

coverage-html: coverage
	$(GO) tool cover -html="$(COVERAGE_FILE)" -o "$(COVERAGE_HTML)"

mod-tidy:
	$(GO) mod tidy

mod-download:
	$(GO) mod download

mod-verify:
	$(GO) mod verify

check: fmt-check test vet build

clean:
	rm -rf "$(BUILD_DIR)" "$(COVERAGE_FILE)" "$(COVERAGE_HTML)"

clean-cache:
	$(GO) clean -testcache

version:
	$(GO) version

$(BUILD_DIR):
	mkdir -p "$@"
