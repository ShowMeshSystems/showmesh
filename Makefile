MODULE      := github.com/showmeshsystems/showmesh
BIN_DIR     := ./bin
COORDINATOR := $(BIN_DIR)/showmesh-coordinator
AGENT       := $(BIN_DIR)/showmesh-agent
MULTISYNC_PROBE := $(BIN_DIR)/showmesh-multisync-probe
SHOWMESHCTL := $(BIN_DIR)/showmeshctl
FPP_PLUGIN  := $(BIN_DIR)/showmesh-fpp-plugin

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
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(FPP_PLUGIN) ./cmd/showmesh-fpp-plugin

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

# test-integration-broker proves the two claims about
# internal/coordinator/broker that need a real broker connection rather than
# a fake: the retained-message discard (response.go's RETAIN check, and the
# RetainAsPublished=false subscription option that makes the distinction
# possible — see that package's doc comments) and, per review finding 1 on
# commit 9dcab74, a response waiter's subscription surviving a real broker
# restart mid-wait. Behind the `integration` build tag, so it is never part
# of `test`/`check`. Unlike test-integration-fpp/fppmqtt, this suite starts
# and tears down its OWN throwaway Mosquitto container per test (see
# internal/coordinator/broker/response_integration_test.go's
# startTestMosquitto) rather than one broker this script starts up front —
# see scripts/test-integration-broker.sh. Review finding 5 on commit
# 9dcab74: before this target existed, this suite's own package doc
# comments documented the `go test -tags=integration` invocation but nothing
# in this Makefile, in scripts/, or in CI ever ran it.
.PHONY: test-integration-broker
test-integration-broker:
	./scripts/test-integration-broker.sh

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
	rm -rf $(BIN_DIR) $(FPP_PLUGIN_DIST)

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

# --- FPP plugin release artifacts (Step 9) ---
#
# The deployed fleet spans both ARM word sizes (a Raspberry Pi 3 B+, a
# BeagleBone Black needing GOARCH=arm GOARM=7, and a PocketBeagle2), and
# CI has so far built linux/amd64 and linux/arm64 container images only —
# neither standalone binaries nor armv7 at all. This section is the
# plugin's own release-artifact pipeline: three CGO_ENABLED=0 static
# cross-compiles, each tarred as a single file named showmesh-fpp-plugin
# at mode 0755, plus one SHA256SUMS covering all three.
#
# The artifact contract is pinned (not invented here) so a separate
# packaging repository's install script can fetch and verify against it
# without coordinating a second change:
#   tag:   fpp-plugin-v<VERSION>
#   asset: showmesh-fpp-plugin_<VERSION>_linux_<ARCH>.tar.gz, ARCH in
#          {amd64, arm64, armv7}
#   sums:  showmesh-fpp-plugin_<VERSION>_SHA256SUMS, standard
#          `sha256sum` format ("<hex>  <filename>")
#
# This section only BUILDS and verifies these artifacts locally; nothing
# here publishes anything. No GitHub Release is created by this Makefile
# or by CI's fpp-plugin-release job — publication is enabled later, when
# an installer needs to reach real hardware, and is deliberately a
# separate decision from building and verifying the pipeline that will
# feed it.
FPP_PLUGIN_DIST    := ./dist/fpp-plugin
FPP_PLUGIN_VERSION ?= $(VERSION)

# Deterministic ldflags for the release build specifically: the pinned
# commit's own commit timestamp, not BUILD_DATE's wall-clock $(shell date
# ...) above, which by construction differs on every invocation and would
# make two builds of the identical commit non-reproducible for no reason
# connected to the actual source. Verified locally (2026-08-14, macOS
# host cross-compiling linux/amd64, go1.25/1.26): two independent
# CGO_ENABLED=0 `go build -trimpath` invocations with these ldflags
# produce byte-identical output — see verify-fpp-plugin-reproducible
# below, which checks this on every run rather than resting on that one
# observation.
FPP_PLUGIN_COMMIT_DATE := $(shell git show -s --format=%cI HEAD 2>/dev/null || echo unknown)
FPP_PLUGIN_LDFLAGS := -X $(MODULE)/internal/version.Version=$(FPP_PLUGIN_VERSION) \
                       -X $(MODULE)/internal/version.Commit=$(COMMIT) \
                       -X $(MODULE)/internal/version.BuildDate=$(FPP_PLUGIN_COMMIT_DATE)

# release-fpp-plugin-<arch> each build one platform's binary at mode
# 0755, tar it alone (no leading directory) under the pinned asset name,
# and remove the loose binary so it cannot be mistaken for one of the
# platform-specific ones on a later run. -trimpath keeps this host's own
# absolute build path out of the binary, which matters for the
# reproducibility check below as much as for not leaking a builder's
# local filesystem layout.
.PHONY: release-fpp-plugin-amd64
release-fpp-plugin-amd64:
	mkdir -p $(FPP_PLUGIN_DIST)
	rm -f $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(FPP_PLUGIN_LDFLAGS)" -o $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin ./cmd/showmesh-fpp-plugin
	chmod 0755 $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin
	tar -C $(FPP_PLUGIN_DIST) -czf $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_linux_amd64.tar.gz showmesh-fpp-plugin
	rm -f $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin

.PHONY: release-fpp-plugin-arm64
release-fpp-plugin-arm64:
	mkdir -p $(FPP_PLUGIN_DIST)
	rm -f $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(FPP_PLUGIN_LDFLAGS)" -o $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin ./cmd/showmesh-fpp-plugin
	chmod 0755 $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin
	tar -C $(FPP_PLUGIN_DIST) -czf $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_linux_arm64.tar.gz showmesh-fpp-plugin
	rm -f $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin

# armv7 is the BeagleBone Black's own word size (GOARCH=arm GOARM=7) —
# the asset name uses "armv7" per the pinned contract above even though
# GOARCH itself is "arm"; RES-015 section 5.3's own warning applies here
# in spirit even though this is a build-time label, not a runtime probe:
# never let "arm" alone stand in for the word size without GOARM alongside
# it.
.PHONY: release-fpp-plugin-armv7
release-fpp-plugin-armv7:
	mkdir -p $(FPP_PLUGIN_DIST)
	rm -f $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin
	GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags "$(FPP_PLUGIN_LDFLAGS)" -o $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin ./cmd/showmesh-fpp-plugin
	chmod 0755 $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin
	tar -C $(FPP_PLUGIN_DIST) -czf $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_linux_armv7.tar.gz showmesh-fpp-plugin
	rm -f $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin

# release-fpp-plugin is the target CI (and the bench) actually run: build
# all three platforms, write the checksums file the pinned contract
# names, then immediately verify it against the tarballs just produced —
# "verify the manifest" happens on every invocation, not as a trusted
# one-time claim. sha256sum -c reads relative filenames from within
# FPP_PLUGIN_DIST, which is why both commands cd there first.
.PHONY: release-fpp-plugin
release-fpp-plugin: release-fpp-plugin-amd64 release-fpp-plugin-arm64 release-fpp-plugin-armv7
	cd $(FPP_PLUGIN_DIST) && sha256sum showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_linux_*.tar.gz > showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_SHA256SUMS
	cd $(FPP_PLUGIN_DIST) && sha256sum -c showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_SHA256SUMS
	@echo "release-fpp-plugin: built and self-verified $(FPP_PLUGIN_DIST)/showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_SHA256SUMS"

# verify-fpp-plugin-reproducible builds ONE platform (amd64 — sufficient
# to prove the mechanism; the other two share the identical build
# invocation shape and gain little from repeating this per platform)
# twice, independently, into two separate directories, and fails unless
# the two binaries are byte-identical. This is the stronger,
# cross-invocation reproducibility claim — release-fpp-plugin's own
# sha256sum -c above only proves the checksums file matches what THIS run
# produced, not that a second run would produce the same thing.
.PHONY: verify-fpp-plugin-reproducible
verify-fpp-plugin-reproducible:
	rm -rf $(FPP_PLUGIN_DIST)/.reproducible-a $(FPP_PLUGIN_DIST)/.reproducible-b
	mkdir -p $(FPP_PLUGIN_DIST)/.reproducible-a $(FPP_PLUGIN_DIST)/.reproducible-b
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(FPP_PLUGIN_LDFLAGS)" -o $(FPP_PLUGIN_DIST)/.reproducible-a/showmesh-fpp-plugin ./cmd/showmesh-fpp-plugin
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(FPP_PLUGIN_LDFLAGS)" -o $(FPP_PLUGIN_DIST)/.reproducible-b/showmesh-fpp-plugin ./cmd/showmesh-fpp-plugin
	@if ! cmp -s $(FPP_PLUGIN_DIST)/.reproducible-a/showmesh-fpp-plugin $(FPP_PLUGIN_DIST)/.reproducible-b/showmesh-fpp-plugin; then \
		echo "two independent builds of the same commit produced DIFFERENT binaries; the release pipeline is not reproducible"; \
		exit 1; \
	fi
	rm -rf $(FPP_PLUGIN_DIST)/.reproducible-a $(FPP_PLUGIN_DIST)/.reproducible-b
	@echo "verify-fpp-plugin-reproducible: OK, two independent builds are byte-identical"

.PHONY: clean-fpp-plugin-dist
clean-fpp-plugin-dist:
	rm -rf $(FPP_PLUGIN_DIST)
