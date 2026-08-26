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

# The agent built by `build` is CGo-free and therefore carries no audio
# engine: internal/agent/audio/gstengine compiles to its !cgo variant,
# which reports itself unavailable. An agent that must play audio is built
# here instead, natively, and needs the GStreamer development headers plus
# the plugins carrying audiomixer.
.PHONY: build-agent-native
build-agent-native:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o $(AGENT)-native ./cmd/showmesh-agent

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

# Refuses to run the UI targets on a Node major other than .nvmrc's, which
# is the one CI uses. Measured 2026-08-16 on macOS: Node 25 and 26 fail 82
# jsdom tests in 3 files with `Expected signal ("AbortSignal {}") to be an
# instance of AbortSignal`, a cross-realm mismatch between jsdom's
# AbortSignal and the undici those majors bundle. The failure names neither
# Node nor the version, so without this guard a shadowed `node` on PATH
# reads as a repository defect.
.PHONY: ui-node-check
ui-node-check:
	@want=$$(cat .nvmrc); \
	have=$$(node -v 2>/dev/null | sed 's/^v//' | cut -d. -f1); \
	if [ -z "$$have" ]; then \
		echo "No node on PATH. This repository targets Node $$want (.nvmrc)."; \
		exit 1; \
	fi; \
	if [ "$$have" != "$$want" ]; then \
		echo "Node $$have is first on PATH; this repository targets Node $$want (.nvmrc)."; \
		echo "Run 'nvm use' here, or put a Node $$want ahead of it on PATH."; \
		echo "  node: $$(command -v node)"; \
		exit 1; \
	fi

.PHONY: ui-install
ui-install: ui-node-check
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

# Checks final repository and GitHub state after the task branch is pushed and
# its PR checks have completed. Local and integration gates remain explicit
# commands because their applicability and evidence must be reported separately.
.PHONY: pr-ready-check
pr-ready-check:
	./scripts/pr-ready-check.sh

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
# connected to the actual source.
FPP_PLUGIN_COMMIT_DATE := $(shell git show -s --format=%cI HEAD 2>/dev/null || echo unknown)
FPP_PLUGIN_LDFLAGS := -X $(MODULE)/internal/version.Version=$(FPP_PLUGIN_VERSION) \
                       -X $(MODULE)/internal/version.Commit=$(COMMIT) \
                       -X $(MODULE)/internal/version.BuildDate=$(FPP_PLUGIN_COMMIT_DATE)

# TAR resolves to a GNU-compatible tar — GNU tar directly (every Linux CI
# runner, including this project's own ubuntu-latest jobs) or Homebrew's
# gtar on macOS (`brew install gnu-tar`) — because the determinism flags
# below (--sort, --owner, --group, --numeric-owner, --mtime) are GNU
# tar's own, not macOS's default bsdtar's. TAR_IS_GNU gates on that at
# parse time rather than failing a local build outright: a machine with
# neither still gets a correct tarball, just not a byte-reproducible one
# across two local runs — see fpp_plugin_build_and_package's else branch.
# What actually has to reproduce is CI's own output, and CI always has
# GNU tar.
TAR := $(shell command -v gtar 2>/dev/null || command -v tar 2>/dev/null)
TAR_IS_GNU := $(shell $(TAR) --version 2>/dev/null | grep -qi 'gnu tar' && echo yes)

# fpp_plugin_build_and_package builds one platform's binary directly into
# directory $(4), chmod 0755, packages it as
# $(4)/showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_linux_$(3).tar.gz, and
# removes the loose binary so it is never mistaken for a different
# platform's on a later run. $(1) is GOARCH, $(2) is any extra build-time
# env (e.g. "GOARM=7", or empty for amd64/arm64), $(3) is the asset
# filename's ARCH label — NOT always equal to $(1): armv7's own GOARCH is
# "arm", and RES-015's own "never let arm alone stand in for the word
# size without GOARM" warning applies here in spirit even though this is
# a build-time label, not a runtime probe.
#
# The tar invocation is this finding's actual fix. Proved broken by
# running release-fpp-plugin twice at one commit and diffing the three
# resulting SHA256SUMS: -trimpath already makes the raw BINARIES
# byte-identical, but a plain `tar -czf` still records each entry's
# mtime, uid, gid, and owner/group NAME (a real local username, which a
# GNU tar running as root on the installing FPP host would go on to
# apply to the extracted file), plus gzip's own header carries a
# timestamp — none of which -trimpath touches, because none of it is in
# the binary. When GNU tar is available: --sort=name orders archive
# members deterministically (moot at one member today, cheap insurance
# against ever adding a second), --owner=0 --group=0 --numeric-owner
# zero the uid/gid and force numeric (never a name) rather than whatever
# this builder's account happens to be, --mtime='@0' zeroes every entry's
# timestamp, and piping the uncompressed stream to `gzip -n` (rather than
# tar's own built-in -z) strips gzip's own embedded name and timestamp
# fields, which GNU tar's -z does not expose a way to suppress. Verified,
# not assumed: verify-fpp-plugin-reproducible below builds and packages
# the same commit twice, independently, and diffs the TARBALLS
# byte-for-byte — not the binaries inside them, which were never what
# this finding was about.
define fpp_plugin_build_and_package
	mkdir -p $(4)
	rm -f $(4)/showmesh-fpp-plugin
	GOOS=linux GOARCH=$(1) $(2) CGO_ENABLED=0 go build -trimpath -ldflags "$(FPP_PLUGIN_LDFLAGS)" -o $(4)/showmesh-fpp-plugin ./cmd/showmesh-fpp-plugin
	chmod 0755 $(4)/showmesh-fpp-plugin
	if [ "$(TAR_IS_GNU)" = "yes" ]; then \
		$(TAR) --sort=name --owner=0 --group=0 --numeric-owner --mtime='@0' -C $(4) -cf - showmesh-fpp-plugin | gzip -n -9 > $(4)/showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_linux_$(3).tar.gz; \
	else \
		echo "WARNING: GNU tar not found on PATH (checked gtar, tar); building linux_$(3)'s tarball WITHOUT deterministic mtime/owner/group stripping. It is still correct, but will not reproduce byte-for-byte across two local runs. Install GNU tar (e.g. 'brew install gnu-tar' on macOS) to get that locally; CI always has GNU tar and always gets it." >&2; \
		tar -C $(4) -czf $(4)/showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_linux_$(3).tar.gz showmesh-fpp-plugin; \
	fi
	rm -f $(4)/showmesh-fpp-plugin
endef

.PHONY: release-fpp-plugin-amd64
release-fpp-plugin-amd64:
	$(call fpp_plugin_build_and_package,amd64,,amd64,$(FPP_PLUGIN_DIST))

.PHONY: release-fpp-plugin-arm64
release-fpp-plugin-arm64:
	$(call fpp_plugin_build_and_package,arm64,,arm64,$(FPP_PLUGIN_DIST))

.PHONY: release-fpp-plugin-armv7
release-fpp-plugin-armv7:
	$(call fpp_plugin_build_and_package,arm,GOARM=7,armv7,$(FPP_PLUGIN_DIST))

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

# verify-fpp-plugin-reproducible builds and packages ONE platform (amd64
# — sufficient to prove the mechanism; the other two share the identical
# build-and-package shape and gain little from repeating this per
# platform) twice, independently, into two separate directories, and
# fails unless the two resulting TARBALLS are byte-identical — the actual
# shipped artifact, not the binary inside it (see
# fpp_plugin_build_and_package's own doc comment for why those are
# different claims and this finding was specifically about the former).
# This is the stronger, cross-invocation reproducibility claim —
# release-fpp-plugin's own sha256sum -c only proves the checksums file
# matches what THIS run produced, not that a second run would produce the
# same thing. Requires GNU tar to actually pass (see TAR_IS_GNU above);
# without it, this target still runs and still tells you honestly that it
# could not confirm reproducibility, rather than silently reporting
# success on a comparison it never made.
.PHONY: verify-fpp-plugin-reproducible
verify-fpp-plugin-reproducible:
	rm -rf $(FPP_PLUGIN_DIST)/.reproducible-a $(FPP_PLUGIN_DIST)/.reproducible-b
	$(call fpp_plugin_build_and_package,amd64,,amd64,$(FPP_PLUGIN_DIST)/.reproducible-a)
	$(call fpp_plugin_build_and_package,amd64,,amd64,$(FPP_PLUGIN_DIST)/.reproducible-b)
	@if [ "$(TAR_IS_GNU)" != "yes" ]; then \
		echo "verify-fpp-plugin-reproducible: SKIPPED the actual byte-for-byte comparison — no GNU tar on PATH, so neither tarball was built deterministically and comparing them would only prove that, not reproducibility" >&2; \
		exit 1; \
	fi
	@if ! cmp -s $(FPP_PLUGIN_DIST)/.reproducible-a/showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_linux_amd64.tar.gz $(FPP_PLUGIN_DIST)/.reproducible-b/showmesh-fpp-plugin_$(FPP_PLUGIN_VERSION)_linux_amd64.tar.gz; then \
		echo "two independent builds of the same commit produced DIFFERENT tarballs; the release artifact is not reproducible"; \
		exit 1; \
	fi
	rm -rf $(FPP_PLUGIN_DIST)/.reproducible-a $(FPP_PLUGIN_DIST)/.reproducible-b
	@echo "verify-fpp-plugin-reproducible: OK, two independent builds produced byte-identical TARBALLS"

.PHONY: clean-fpp-plugin-dist
clean-fpp-plugin-dist:
	rm -rf $(FPP_PLUGIN_DIST)

# --- Node agent install bundle (SM-43) ---
#
# The native agent (build-agent-native, CGO_ENABLED=1: go-gst + libltc,
# ADR-042) has no install path of its own. This packages that binary
# together with deploy/node's systemd unit, env template, preflight
# script, installer, and README into one tarball a node operator fetches
# and unpacks, mirroring release-fpp-plugin's discipline (deterministic
# build, GNU tar, gzip -n, sha256sum) but NOT its multi-platform cross
# compile: build-agent-native is CGO_ENABLED=1 and links host C libraries
# (go-gst, libltc), so it cannot be cross-compiled the way the CGO-free
# FPP plugin is. It therefore builds for the host's own platform, on a
# Debian 13+ host with the packages deploy/node/README.md names: amd64
# for this project's CI and bench, arm64 when packaging on an arm64 host
# for a Raspberry Pi class node.
#
# Artifact contract, pinned here as the FPP plugin's own comment pins
# its contract:
#   asset: showmesh-node-agent_<VERSION>_<GOOS>_<GOARCH>.tar.gz
#   sums:  showmesh-node-agent_<VERSION>_SHA256SUMS, covering EVERY
#          platform tarball present at this VERSION (glob, like
#          release-fpp-plugin's), so packaging a second platform into
#          the same dist directory does not leave a sums file naming
#          only the last one.
# Nothing here publishes a GitHub Release; this only builds and
# self-verifies the artifact locally, exactly like release-fpp-plugin.
NODE_AGENT_DIST    := ./dist/node-agent
NODE_AGENT_VERSION ?= $(VERSION)
# The cgo agent links host C libraries, so the target platform is always
# the host's own. Naming the tarball from `go env` keeps an arm64 build
# from claiming to be linux_amd64. One `go env` call, guarded like every
# other parse-time shell here: this runs on EVERY make invocation,
# including `make help` on a host with no Go, and must not print an
# error there. An empty result is caught in the recipe rather than
# silently producing a `__` tarball.
NODE_AGENT_PLATFORM := $(shell go env GOOS GOARCH 2>/dev/null | paste -sd_ - )
NODE_AGENT_TARBALL  := showmesh-node-agent_$(NODE_AGENT_VERSION)_$(NODE_AGENT_PLATFORM).tar.gz
NODE_AGENT_COMMIT_DATE := $(shell git show -s --format=%cI HEAD 2>/dev/null || echo unknown)
NODE_AGENT_LDFLAGS := -X $(MODULE)/internal/version.Version=$(NODE_AGENT_VERSION) \
                       -X $(MODULE)/internal/version.Commit=$(COMMIT) \
                       -X $(MODULE)/internal/version.BuildDate=$(NODE_AGENT_COMMIT_DATE)

# package-node-agent builds the native agent with -trimpath (byte-stable
# paths, same reasoning as the FPP plugin's build) and the deterministic
# commit-timestamp ldflags above, stages it alongside deploy/node's
# install files under a single top-level directory (so the tarball
# extracts to one directory, not loose files mixed with whatever else is
# in the target directory), and archives it with the same GNU
# tar/gzip -n discipline fpp_plugin_build_and_package uses and for the
# same reason: a plain `tar -czf` embeds this builder's uid/gid/owner
# name and per-entry mtimes, and gzip's own header carries a timestamp,
# none of which -trimpath touches because none of it is in the binary.
.PHONY: package-node-agent
package-node-agent:
	@if [ "$(NODE_AGENT_PLATFORM)" = "_" ] || [ -z "$(NODE_AGENT_PLATFORM)" ]; then \
		echo "package-node-agent: could not determine the target platform ('go env GOOS GOARCH' produced nothing). Install Go and put it on PATH; refusing to package an unnamed platform." >&2; \
		exit 1; \
	fi
	mkdir -p $(NODE_AGENT_DIST)
	rm -rf $(NODE_AGENT_DIST)/showmesh-node-agent
	mkdir -p $(NODE_AGENT_DIST)/showmesh-node-agent
	CGO_ENABLED=1 go build -trimpath -ldflags "$(NODE_AGENT_LDFLAGS)" -o $(NODE_AGENT_DIST)/showmesh-node-agent/showmesh-agent-native ./cmd/showmesh-agent
	chmod 0755 $(NODE_AGENT_DIST)/showmesh-node-agent/showmesh-agent-native
	cp deploy/node/showmesh-agent.service deploy/node/agent.env.example deploy/node/preflight.sh deploy/node/install.sh deploy/node/README.md $(NODE_AGENT_DIST)/showmesh-node-agent/
	chmod 0755 $(NODE_AGENT_DIST)/showmesh-node-agent/preflight.sh $(NODE_AGENT_DIST)/showmesh-node-agent/install.sh
	if [ "$(TAR_IS_GNU)" = "yes" ]; then \
		$(TAR) --sort=name --owner=0 --group=0 --numeric-owner --mtime='@0' -C $(NODE_AGENT_DIST) -cf - showmesh-node-agent | gzip -n -9 > $(NODE_AGENT_DIST)/$(NODE_AGENT_TARBALL); \
	else \
		echo "WARNING: GNU tar not found on PATH (checked gtar, tar); building the node agent tarball WITHOUT deterministic mtime/owner/group stripping. It is still correct, but will not reproduce byte-for-byte across two local runs. Install GNU tar (e.g. 'brew install gnu-tar' on macOS) to get that locally; CI always has GNU tar." >&2; \
		tar -C $(NODE_AGENT_DIST) -czf $(NODE_AGENT_DIST)/$(NODE_AGENT_TARBALL) showmesh-node-agent; \
	fi
	rm -rf $(NODE_AGENT_DIST)/showmesh-node-agent
	cd $(NODE_AGENT_DIST) && sha256sum showmesh-node-agent_$(NODE_AGENT_VERSION)_*.tar.gz > showmesh-node-agent_$(NODE_AGENT_VERSION)_SHA256SUMS
	cd $(NODE_AGENT_DIST) && sha256sum -c showmesh-node-agent_$(NODE_AGENT_VERSION)_SHA256SUMS
	@echo "package-node-agent: built and self-verified $(NODE_AGENT_DIST)/showmesh-node-agent_$(NODE_AGENT_VERSION)_SHA256SUMS"

.PHONY: clean-node-agent-dist
clean-node-agent-dist:
	rm -rf $(NODE_AGENT_DIST)

# bench-audio builds and runs bench/audio-node's Track C seam C0a-1
# measurement bench (RES-007): a real Debian 13 GStreamer image, all
# runs against virtual/null devices and captured files, never against
# this host's own audio hardware. Bench scaffolding only, exactly like
# bench/fpp-multisync — not part of `make check` or CI, and not imported
# by internal/ or pkg/. See bench/audio-node/README.md.
.PHONY: bench-audio
bench-audio:
	docker build -t showmesh-bench-audio:dev bench/audio-node
	mkdir -p bench/audio-node/results
	docker run --rm \
		-v $(CURDIR)/bench/audio-node:/work \
		-v audio-bench-scratch:/tmp/audio-bench-scratch \
		-w /work \
		showmesh-bench-audio:dev -c "bash run_all.sh"
