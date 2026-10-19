# HELP
# This will output the help for each task
# thanks to https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html

.DEFAULT_GOAL := help

.PHONY: compat help all build examples fmt fmt-check lint lint-ci vet vuln test test-min coverage cover fuzz fuzz-long chaos soak check cross clean interop test-interop e2e deps gen gen-check schemas-check schemas-sync
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
COVER_PKGS := $(shell go list ./... | grep -v '/examples' | grep -v '/e2e$$' | grep -v '/internal/gen' | grep -v '/internal/specsync' | grep -v '/internal/schematest' | grep -v '/interop$$' | grep -v '/interop/' | paste -sd, -)

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
RUN_EXAMPLES := ocpp16 ocpp201 ocpp21 observability csms station-simulator smart-charging security-profiles device-model remote-management vendor-extensions

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

# CI also tests with the oldest Go that go.mod allows, and the standard
# library's behaviour differs between releases (error types, for one).
test-min: ## Run the library tests with the Go version of go.mod (what CI's oldest job uses)
	@echo "Running tests with go$(GO_MOD_VERSION).0"
	@GOTOOLCHAIN=go$(GO_MOD_VERSION).0 go test -count=1 $(TEST_PKGS)

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
# Each entry is <package dir>:<fuzz target>. An input that fails is written
# to <package>/testdata/fuzz/<target>/ by go test: commit it, so that every
# later test run checks it as a regression.
FUZZTIME ?= 30s
FUZZ_TARGETS := ocppj:FuzzDecode internal/engine:FuzzEndpoint server:FuzzHandshake \
	ocpp16:FuzzDispatch ocpp201:FuzzDispatch ocpp21:FuzzDispatch
fuzz: ## Run the fuzz targets for FUZZTIME each (default 30s)
	@echo "Fuzzing (FUZZTIME=$(FUZZTIME))"
	@for t in $(FUZZ_TARGETS); do \
		go test -run='^$$' -fuzz="^$${t#*:}$$" -fuzztime=$(FUZZTIME) "./$${t%%:*}" || exit 1; \
	done

fuzz-long: ## Run every fuzz target for 10 minutes (what the nightly Hardening workflow runs)
	@$(MAKE) --no-print-directory fuzz FUZZTIME=10m

# Chaos and soak: the seeded chaos proxy test (e2e/chaos_test.go) and the
# parallel and exhaustion tests, with the race detector. Replay a failure
# with the seed it logged: make chaos CHAOS_SEED=<seed>.
CHAOS_DURATION ?= 30s
CHAOS_SEED ?=
SOAKTIME ?= 10m
chaos: ## Run the chaos test for CHAOS_DURATION (default 30s), optionally with CHAOS_SEED
	@echo "Chaos for $(CHAOS_DURATION)"
	@OCPP_CHAOS_DURATION=$(CHAOS_DURATION) OCPP_CHAOS_SEED=$(CHAOS_SEED) \
		go test -race -count=1 -timeout=0 -run '^TestChaos$$' -v ./e2e

soak: ## Chaos for SOAKTIME (default 10m), then the parallel and exhaustion tests 20 times
	@$(MAKE) --no-print-directory chaos CHAOS_DURATION=$(SOAKTIME)
	@echo "Parallel and exhaustion tests, 20 times"
	@go test -race -count=20 -timeout=0 \
		-run '^(TestParallel.*|TestCloseUnderLoad|TestReplacementStorm|TestShutdownUnderLoad|TestSlowHandshake|TestStationFlood|TestOversizedMessage|TestCompressionBomb|TestIncomingFlood|TestQueueSaturation|TestSlowReader|TestKeepalive|TestMatrix)$$' ./e2e
	@go test -race -count=20 -timeout=0 -run '^(TestCallRacingClose|TestPing|TestMaxBadMessages)$$' ./internal/engine

# Reference implementations for 'make interop': otfabric/ocpp-interop v0.4.0
# (The Mobility House ocpp 2.1.0, ChargeTimeEU Java-OCA-OCPP v2.0,
# lorenzodonini/ocpp-go v0.19.0: OCPP 1.6J and 2.0.1 in all three, 2.1 in
# The Mobility House; EVerest libocpp 2026.02.1, a station for 1.6J, 2.0.1
# and 2.1; CitrineOS core v2.0.1, a CSMS for 1.6J, 2.0.1 and 2.1), pinned by multi-arch index digest. Keep in step with interop/harness.go and
# .github/workflows/interop.yml.
# A reference image that cannot be had fails the run: asked to test against
# it, passing by skipping would say nothing.
# Override to test other builds, e.g. the local ones of an ocpp-interop checkout:
#   make interop OCPP_INTEROP_MOBILITYHOUSE_IMAGE=ghcr.io/otfabric/ocpp-interop-mobilityhouse:dev
OCPP_INTEROP_MOBILITYHOUSE_IMAGE ?= ghcr.io/otfabric/ocpp-interop-mobilityhouse@sha256:c77dcac278296ab49c1df8540d982041a85dbbb2248711e5028bbde1d84e9193
OCPP_INTEROP_CHARGETIME_IMAGE    ?= ghcr.io/otfabric/ocpp-interop-chargetime@sha256:c694c6cfdafd5705c2eb2abe3e572e1161c2d047b3d9ef9dab24f70230e71b62
OCPP_INTEROP_LORENZODONINI_IMAGE ?= ghcr.io/otfabric/ocpp-interop-lorenzodonini@sha256:8667321b6fc1e53e15052da41f1684f74a34d38e1b8578af8d5a1a612d901219
OCPP_INTEROP_EVEREST_IMAGE       ?= ghcr.io/otfabric/ocpp-interop-everest@sha256:cbc7793935c9b9c082af6aef2bbf251c08ad5c090e2e7ac7153e474f82828f8d
OCPP_INTEROP_CITRINEOS_IMAGE     ?= ghcr.io/otfabric/ocpp-interop-citrineos@sha256:fc4a41079a5efc6b7a6e7f6450f120d8838f50fefbc73fbb64cd9a51d84a4488

test-interop: ## Run the interop suite against the ocpp-interop reference images (needs Docker)
	@echo "Running interop tests against $(OCPP_INTEROP_MOBILITYHOUSE_IMAGE), $(OCPP_INTEROP_CHARGETIME_IMAGE), $(OCPP_INTEROP_LORENZODONINI_IMAGE), $(OCPP_INTEROP_EVEREST_IMAGE) and $(OCPP_INTEROP_CITRINEOS_IMAGE)"
	@OCPP_INTEROP_MOBILITYHOUSE_IMAGE="$(OCPP_INTEROP_MOBILITYHOUSE_IMAGE)" \
		OCPP_INTEROP_CHARGETIME_IMAGE="$(OCPP_INTEROP_CHARGETIME_IMAGE)" \
		OCPP_INTEROP_LORENZODONINI_IMAGE="$(OCPP_INTEROP_LORENZODONINI_IMAGE)" \
		OCPP_INTEROP_EVEREST_IMAGE="$(OCPP_INTEROP_EVEREST_IMAGE)" \
		OCPP_INTEROP_CITRINEOS_IMAGE="$(OCPP_INTEROP_CITRINEOS_IMAGE)" \
		OCPP_INTEROP_REQUIRE=1 \
		go test -tags=interop -count=1 -timeout=40m ./interop/...

interop: test-interop ## Alias for test-interop

compat: ## Rewrite docs/compatibility (one action matrix per OCPP version) from the reference images (needs Docker)
	@OCPP_WRITE_COMPAT=1 OCPP_INTEROP_REQUIRE=1 \
		OCPP_INTEROP_MOBILITYHOUSE_IMAGE=$(OCPP_INTEROP_MOBILITYHOUSE_IMAGE) \
		OCPP_INTEROP_CHARGETIME_IMAGE=$(OCPP_INTEROP_CHARGETIME_IMAGE) \
		OCPP_INTEROP_LORENZODONINI_IMAGE=$(OCPP_INTEROP_LORENZODONINI_IMAGE) \
		OCPP_INTEROP_EVEREST_IMAGE=$(OCPP_INTEROP_EVEREST_IMAGE) \
		OCPP_INTEROP_CITRINEOS_IMAGE=$(OCPP_INTEROP_CITRINEOS_IMAGE) \
		go test -tags=interop -count=1 -run '^TestWriteCompatibility$$' ./interop/

gen: ## Generate the version packages from the OCA schemas (internal/gen)
	@echo "Generating ocpp16, ocpp201 and ocpp21"
	@go generate ./ocpp16 ./ocpp201 ./ocpp21

GENERATED := ocpp16/zz_generated_types.go ocpp16/zz_generated_actions.go ocpp16/coverage.json \
	ocpp201/zz_generated_types.go ocpp201/zz_generated_actions.go ocpp201/coverage.json \
	ocpp21/zz_generated_types.go ocpp21/zz_generated_actions.go ocpp21/coverage.json

gen-check: ## Fail if regenerating changes the generated files (they are then updated)
	@echo "Checking the generated code"
	@tmp="$$(mktemp -d)"; \
	for f in $(GENERATED); do mkdir -p "$$tmp/$$(dirname $$f)"; cp "$$f" "$$tmp/$$f"; done; \
	go generate ./ocpp16 ./ocpp201 ./ocpp21 || exit 1; \
	stale=""; for f in $(GENERATED); do cmp -s "$$f" "$$tmp/$$f" || stale="$$stale $$f"; done; \
	rm -rf "$$tmp"; \
	if [ -n "$$stale" ]; then echo "Generated code was out of date:$$stale (now regenerated: review and commit)"; exit 1; fi

schemas-sync: ## Refresh schemas/ from the OCA download page (review the diff; see schemas/README.md)
	@go run ./internal/specsync

schemas-check: ## Check the vendored OCA schemas against their recorded SHA-256 sums
	@cd schemas && shasum -a 256 -c --quiet SHA256SUMS && echo "schemas: OK"

check: fmt fmt-check deps gen-check schemas-check lint lint-ci vet vuln test test-min coverage cross examples ## Run format + generate + lint + vet + vuln + test + cross-build

clean: ## Remove generated binaries and coverage artifacts
	@echo "Cleaning up"
	@rm -rf $(BIN_DIR) coverage.out coverage.html
