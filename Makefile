MODULE      := github.com/showmeshsystems/showmesh
BIN_DIR     := ./bin
COORDINATOR := $(BIN_DIR)/showmesh-coordinator
AGENT       := $(BIN_DIR)/showmesh-agent
MULTISYNC_PROBE := $(BIN_DIR)/showmesh-multisync-probe
SHOWMESHCTL := $(BIN_DIR)/showmeshctl

VERSION     ?= dev
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) \
           -X $(MODULE)/internal/version.Commit=$(COMMIT) \
           -X $(MODULE)/internal/version.BuildDate=$(BUILD_DATE)

.PHONY: build
build:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(COORDINATOR) ./cmd/showmesh-coordinator
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(AGENT) ./cmd/showmesh-agent
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(MULTISYNC_PROBE) ./cmd/showmesh-multisync-probe
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(SHOWMESHCTL) ./cmd/showmeshctl

.PHONY: test
test:
	go test -count=1 ./...

# test-integration proves the Step 2 acceptance criteria that cannot be
# proven against a fake: an agent appearing in coordinator inventory, an
# unclean kill's Last Will, and a coordinator restoring state from retained
# topics on restart (plus the retained-message freshness rule underneath
# all three). Behind the `integration` build tag, so it is never part of
# `test`/`check` and never needs a broker to run those. See
# scripts/test-integration.sh (which this delegates to for starting and
# tearing down a throwaway Mosquitto broker) and test/integration's package
# doc comment.
.PHONY: test-integration
test-integration:
	./scripts/test-integration.sh

# test-integration-fpp proves the FPP REST collector's claims that can only
# be proven against a real fppd: a live poll matching the daemon's actual
# reported state, and — the collector's most important behavior — that
# losing the FPP produces collection_failed evidence, never a stale
# `current` reading and never a fabricated `false` for
# fpp.multisync.enabled. Behind the `integration` build tag, so it is never
# part of `test`/`check`, and deliberately not part of CI: the bench FPP is
# a multi-gigabyte source build on first run. See
# scripts/test-integration-fpp.sh (which starts bench/fpp-multisync's
# fpp-master if it is not already running, and unlike test-integration.sh's
# throwaway Mosquitto, leaves it running afterward — see that script for
# why) and internal/coordinator/collector/fpp's integration_test.go.
.PHONY: test-integration-fpp
test-integration-fpp:
	./scripts/test-integration-fpp.sh

# test-integration-fppmqtt proves the FPP MQTT collector's claims that can
# only be proven against a real broker connection: that Collector.Run's
# actual autopaho wiring (connect, subscribeAll, the OnPublishReceived
# handler) delivers messages into Collector.Poll's output end to end, and
# that the retained/live evidence-age distinction (contract section 4.2)
# holds over a real connection, not just against a directly-injected
# handler call. Behind the `integration` build tag, so it is never part of
# `test`/`check`. Unlike test-integration-fpp, this needs nothing but a
# throwaway broker: internal/coordinator/collector/fppmqtt is a pure MQTT
# client, never an HTTP one. See scripts/test-integration-fppmqtt.sh (which
# starts and tears down a throwaway Mosquitto, exactly like
# test-integration.sh's own) and
# internal/coordinator/collector/fppmqtt/integration_test.go's package doc
# comment.
.PHONY: test-integration-fppmqtt
test-integration-fppmqtt:
	./scripts/test-integration-fppmqtt.sh

GOLANGCI_LINT_VERSION := v2.6.2

.PHONY: lint
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo "using installed golangci-lint"; \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not on PATH; using go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)"; \
		go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...; \
	fi

.PHONY: tidy
tidy:
	go mod tidy

# Go source lives only under these four directories; listing them
# explicitly, rather than gofmt'ing ".", is what keeps this scoped to the
# project's own code now that ui/ exists alongside it. gofmt is a plain
# filesystem walk with no notion of Go module boundaries (unlike
# `go build ./...`, which the ui/go.mod nested-module marker already keeps
# out of ui/node_modules) — found during Step 4, when `gofmt -l .` walked
# into ui/node_modules and would have flagged or silently reformatted a
# vendored third-party .go file some npm package happens to ship.
GOFMT_DIRS := cmd internal pkg test

.PHONY: fmt
fmt:
	gofmt -w $(GOFMT_DIRS)

.PHONY: fmt-check
fmt-check:
	@unformatted="$$(gofmt -l $(GOFMT_DIRS))"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needs to be run on:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

.PHONY: vet
vet:
	go vet ./...

.PHONY: clean
clean:
	rm -rf $(BIN_DIR)

.PHONY: run-coordinator
run-coordinator: build
	$(COORDINATOR)

# Operator UI (ADR-014, ADR-015). Deliberately NOT a prerequisite of `test`
# or `build` above: those two Go-only targets must keep working on a
# machine with no Node installed at all, which is also why CI runs the Go
# job and the UI job as separate jobs rather than one. `check` is the
# exception, folding the UI in on purpose (spec section 4.5) so a
# constraint violation in either half fails the same command an operator
# or CI would actually run before trusting a change.
#
# ui-install is a prerequisite of every other ui-* target rather than
# something a developer has to remember to run first; `npm ci` against a
# committed, unmodified package-lock.json is fast when node_modules is
# already current and is what CI needs to trust the exact locked tree
# rather than whatever `npm install` would resolve today.
.PHONY: ui-install
ui-install:
	cd ui && npm ci

.PHONY: ui-lint
ui-lint: ui-install
	cd ui && npm run lint

.PHONY: ui-test
ui-test: ui-install
	cd ui && npm test

.PHONY: ui-build
ui-build: ui-install
	cd ui && npm run build

# Regenerates ui/src/api/generated/schema.d.ts from api/openapi.yaml and
# fails if that regeneration produces a diff against what's committed.
# This is what makes ADR-015's "generated from or verified against the Go
# types" a checked property instead of a one-time claim: api/openapi.yaml
# is itself conformance-tested against real coordinator responses, so a
# client type that has drifted from the spec has drifted from the Go types
# it describes, and this target is what catches that before it ships.
.PHONY: ui-gen-check
ui-gen-check: ui-install
	cd ui && npm run gen:api
	@if ! git diff --quiet -- ui/src/api/generated/schema.d.ts; then \
		echo "ui/src/api/generated/schema.d.ts is stale relative to api/openapi.yaml."; \
		echo "Run 'npm run gen:api' in ui/ and commit the result."; \
		git diff -- ui/src/api/generated/schema.d.ts; \
		exit 1; \
	fi

.PHONY: check
check: fmt-check vet lint test ui-lint ui-test ui-build ui-gen-check

IMAGE       ?= showmesh
UI_IMAGE    ?= showmesh-operator-ui
DOCKER      ?= docker

.PHONY: docker-build
docker-build:
	$(DOCKER) build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE):$(VERSION) .
	$(DOCKER) build -t $(UI_IMAGE):$(VERSION) ./ui

.PHONY: docker-run
docker-run:
	$(DOCKER) run --rm -p 8080:8080 $(IMAGE):$(VERSION)
