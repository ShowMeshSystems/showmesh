MODULE      := github.com/showmeshsystems/showmesh
BIN_DIR     := ./bin
COORDINATOR := $(BIN_DIR)/showmesh-coordinator
AGENT       := $(BIN_DIR)/showmesh-agent
MULTISYNC_PROBE := $(BIN_DIR)/showmesh-multisync-probe

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

.PHONY: test
test:
	go test ./...

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

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: fmt-check
fmt-check:
	@unformatted="$$(gofmt -l .)"; \
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

.PHONY: check
check: fmt-check vet lint test

IMAGE       ?= showmesh
DOCKER      ?= docker

.PHONY: docker-build
docker-build:
	$(DOCKER) build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE):$(VERSION) .

.PHONY: docker-run
docker-run:
	$(DOCKER) run --rm -p 8080:8080 $(IMAGE):$(VERSION)
