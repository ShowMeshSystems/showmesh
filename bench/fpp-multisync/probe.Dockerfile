# Dockerfile for showmesh-multisync-probe, bench-only.
#
# This is deliberately separate from the root Dockerfile, which builds the
# shipped showmesh-coordinator image and must not acquire bench concerns
# (see bench/fpp-multisync/README.md and CLAUDE.md's repo map). This file
# follows the same conventions as the root Dockerfile -- multi-stage,
# CGO_ENABLED=0, -trimpath, distroless nonroot runtime -- for consistency,
# not because the probe ships anywhere.
#
# Build context is the repository root (see docker-compose.yml's
# `build.context: ../..`), because the probe imports internal/version and
# pkg/multisync alongside its own cmd/ package.

FROM --platform=$BUILDPLATFORM golang:1.26.5-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY pkg/ ./pkg/

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags "-s -w \
      -X github.com/showmeshsystems/showmesh/internal/version.Version=${VERSION} \
      -X github.com/showmeshsystems/showmesh/internal/version.Commit=${COMMIT} \
      -X github.com/showmeshsystems/showmesh/internal/version.BuildDate=${BUILD_DATE}" \
    -o /out/showmesh-multisync-probe \
    ./cmd/showmesh-multisync-probe

# Pre-create the capture output directory with the runtime UID so a bind
# mount or named volume at /captures is writable by the nonroot user below,
# the same reasoning the root Dockerfile applies to /var/lib/showmesh.
RUN mkdir -p /out/captures && chown -R 65532:65532 /out/captures

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/showmesh-multisync-probe /usr/local/bin/showmesh-multisync-probe
COPY --from=builder --chown=65532:65532 /out/captures /captures
COPY LICENSE /usr/share/doc/showmesh/LICENSE

USER 65532:65532

WORKDIR /captures

ENTRYPOINT ["/usr/local/bin/showmesh-multisync-probe"]

LABEL org.opencontainers.image.source="https://github.com/ShowMeshSystems/showmesh" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="showmesh-multisync-probe (bench)" \
      org.opencontainers.image.description="RES-002 bench instrument: not part of the shipped ShowMesh product. See bench/fpp-multisync/README.md."
