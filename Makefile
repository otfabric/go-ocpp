# HELP
# This will output the help for each task
# thanks to https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html

.DEFAULT_GOAL := help

.PHONY: help all build examples fmt fmt-check lint lint-ci vet vuln test coverage cover fuzz check cross clean interop test-interop e2e deps gen gen-check schemas-check
help: ## This help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Parent otfabric/go.work does not list this module; isolate toolchain commands.
export GOWORK := off

# CI formats and lints with the Go version from go.mod, not with the toolchain installed here.
# gofmt's output changes between Go releases, so formatting is done with that version, and
# `fmt-check` also requires the local gofmt to agree.
GO_MOD_VERSION := $(shell sed -nE 's/^go ([0-9]+\.[0-9]+).*/\1/p' go.mod | head -n1)
GOFMT_CI = $(shell GOTOOLCHAIN=go$(GO_MOD_VERSION).0 go env GOROOT)/bin/gofmt

# Output directory for generated binaries
BIN_DIR := bin
# Library packages: tests and coverage (exclude examples and the interop suite)
TEST_PKGS := $(shell go list ./... | grep -v '/examples' | grep -v '/interop$$' | grep -v '/interop/')
# Coverage is measured over the library packages, whichever package's tests exercise them.
COVER_PKGS := $(shell go list ./... | grep -v '/examples' | grep -v '/e2e$$' | grep -v '/internal/gen' | grep -v '/internal/schematest' | grep -v '/interop$$' | grep -v '/interop/' | paste -sd, -)

# Platforms the library must build on, pure Go (CGO_ENABLED=0).
CROSS_PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

all: build ## Default target: build the examples

build: ## Build the library and the examples into ./bin
	@echo "Building"
	@go build ./...
	@mkdir -p $(BIN_DIR)
	@for dir in examples/*/; do \
		[ -d "$$dir" ] || continue; \
		name="$$(basename "$$dir")"; \
		go build -o "$(BIN_DIR)/example-$$name" "./$$dir"; \
	done

# The self-contained examples: each starts both sides and exits by itself.
RUN_EXAMPLES := ocpp16 ocpp201

examples: ## Run the self-contained examples (each must exit successfully)
	@for e in $(RUN_EXAMPLES); do \
		echo "Running example $$e"; \
		go run ./examples/$$e >/dev/null || { echo "example $$e failed"; exit 1; }; \
	done

cross: ## Build the library for every supported OS and architecture without cgo
	@for p in $(CROSS_PLATFORMS); do \
		echo "Building for $$p"; \
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build ./... || exit 1; \
	done

fmt: ## Format Go code with the gofmt of the Go version in go.mod (what CI uses)
	@echo "Running gofmt (go$(GO_MOD_VERSION))"
	@$(GOFMT_CI) -w .

fmt-check: ## Fail if the go.mod-version gofmt or the local gofmt would change any file
	@echo "Checking formatting (gofmt go$(GO_MOD_VERSION) and local $$(go env GOVERSION))"
	@ci="$$($(GOFMT_CI) -l .)"; loc="$$(gofmt -l .)"; \
	if [ -n "$$ci$$loc" ]; then \
		[ -z "$$ci" ] || { echo "Not formatted for gofmt go$(GO_MOD_VERSION) (CI):"; echo "$$ci"; }; \
		[ -z "$$loc" ] || { echo "Not formatted for the local gofmt:"; echo "$$loc"; }; \
		echo "Files must be stable under both. Run 'make fmt'."; \
		exit 1; \
	fi

lint: ## Run staticcheck (includes -tags=interop for ./interop)
	@echo "Running staticcheck"
	@staticcheck -tags=interop ./...

lint-ci: ## Run golangci-lint (see .golangci.yml)
	@echo "Running golangci-lint"
	@golangci-lint run ./...

vet: ## Run go vet
	@echo "Running go vet"
	@go vet -tags=interop ./...

vuln: ## Run govulncheck
	@echo "Running govulncheck"
	@govulncheck ./...

# ADR 0002 allows exactly one production dependency, github.com/coder/websocket.
# ADR 0003 allows a reference JSON Schema validator for tests only (imported
# by internal/schematest, which only the version packages' tests import),
# with what it requires. Anything else needs an ADR first.
ALLOWED_DEPS := github.com/coder/websocket github.com/santhosh-tekuri/jsonschema/v6 golang.org/x/text
deps: ## Fail if go.mod directly requires anything not allowed by an ADR
	@echo "Checking dependencies"
	@deps="$$(go list -m -f '{{if and (not .Main) (not .Indirect)}}{{.Path}}{{end}}' all | grep -v -x -F $(foreach d,$(ALLOWED_DEPS),-e $(d)) || true)"; \
	if [ -n "$$deps" ]; then echo "go.mod requires modules no ADR allows:"; echo "$$deps"; exit 1; fi

test: ## Run the library tests with the race detector
	@echo "Running tests"
	@go test -count=1 -race $(TEST_PKGS)

e2e: ## Run the end-to-end tests of this module's client against this module's server
	@echo "Running end-to-end tests"
	@if [ -d e2e ]; then go test -count=1 -race ./e2e/...; else echo "no e2e tests yet (Phase 2)"; fi

coverage: ## Run tests with coverage on the library packages (writes coverage.out)
	@echo "Running coverage"
	@go test -count=1 -race -coverprofile=coverage.out -covermode=atomic -coverpkg=$(COVER_PKGS) $(TEST_PKGS)
	@go tool cover -func=coverage.out | tail -n1

cover: coverage ## Open coverage report in browser
	@echo "Opening coverage report"
	@go tool cover -html=coverage.out

# Duration each fuzz target runs. Override with FUZZTIME=... (e.g. FUZZTIME=5m).
# Each entry is <package dir>:<fuzz target>. The dispatchers of the version
# packages are added in Phases 3, 5 and 6.
FUZZTIME ?= 30s
FUZZ_TARGETS := ocppj:FuzzDecode ocpp16:FuzzDispatch ocpp201:FuzzDispatch
fuzz: ## Run the fuzz targets for FUZZTIME each (default 30s)
	@echo "Fuzzing (FUZZTIME=$(FUZZTIME))"
	@for t in $(FUZZ_TARGETS); do \
		go test -run='^$$' -fuzz="^$${t#*:}$$" -fuzztime=$(FUZZTIME) "./$${t%%:*}" || exit 1; \
	done

# Reference implementations for 'make interop': otfabric/ocpp-interop v0.0.1
# (The Mobility House ocpp 2.1.0, ChargeTimeEU Java-OCA-OCPP v2.0), pinned by
# multi-arch index digest. Keep in step with interop/harness.go and
# .github/workflows/interop.yml.
# A reference image that cannot be had fails the run: asked to test against
# it, passing by skipping would say nothing.
# Override to test other builds, e.g. the local ones of an ocpp-interop checkout:
#   make interop OCPP_INTEROP_MOBILITYHOUSE_IMAGE=ghcr.io/otfabric/ocpp-interop-mobilityhouse:dev
OCPP_INTEROP_MOBILITYHOUSE_IMAGE ?= ghcr.io/otfabric/ocpp-interop-mobilityhouse@sha256:6243da691f8682398a8cd420fb91b68a4d0f6aa42267961f8d04d097d172fd00
OCPP_INTEROP_CHARGETIME_IMAGE    ?= ghcr.io/otfabric/ocpp-interop-chargetime@sha256:beeb0e2b34d3c56b5835ed280b38e52ab4d1eba86251552e72e33bf12b7e3b2e

test-interop: ## Run the interop suite against the ocpp-interop reference images (needs Docker)
	@echo "Running interop tests against $(OCPP_INTEROP_MOBILITYHOUSE_IMAGE) and $(OCPP_INTEROP_CHARGETIME_IMAGE)"
	@OCPP_INTEROP_MOBILITYHOUSE_IMAGE="$(OCPP_INTEROP_MOBILITYHOUSE_IMAGE)" \
		OCPP_INTEROP_CHARGETIME_IMAGE="$(OCPP_INTEROP_CHARGETIME_IMAGE)" \
		OCPP_INTEROP_REQUIRE=1 \
		go test -tags=interop -count=1 -timeout=20m ./interop/...

interop: test-interop ## Alias for test-interop

gen: ## Generate the version packages from the OCA schemas (internal/gen)
	@echo "Generating ocpp16 and ocpp201"
	@go generate ./ocpp16 ./ocpp201

gen-check: gen ## Fail if the generated code differs from what is committed
	@if ! git diff --quiet -- ocpp16 ocpp201; then \
		echo "Generated code is out of date: run 'make gen' and commit."; git --no-pager diff --stat -- ocpp16 ocpp201; exit 1; \
	fi

schemas-check: ## Check the vendored OCA schemas against their recorded SHA-256 sums
	@cd schemas && shasum -a 256 -c --quiet SHA256SUMS && echo "schemas: OK"

check: fmt fmt-check deps gen-check schemas-check lint lint-ci vet vuln test coverage cross examples ## Run format + generate + lint + vet + vuln + test + cross-build

clean: ## Remove generated binaries and coverage artifacts
	@echo "Cleaning up"
	@rm -rf $(BIN_DIR) coverage.out coverage.html
