# Dockerfile for the ShowMesh coordinator.
#
# Only the coordinator is containerized. Node agents run natively on the
# media-node host (see ARCHITECTURE.md section 10.2) because they need
# direct access to GPU, HDMI, audio, EDID, and NDI, none of which are
# practical to pass through a container reliably across platforms.
#
# Multi-stage, buildx-aware build: --platform=$BUILDPLATFORM on the builder
# stage means the compiler always runs as the *build* host's native
# architecture (fast, no QEMU emulation), while GOOS/GOARCH cross-compile
# the *target* binary. Go's cross-compilation is native and CGO is disabled,
# so this produces correct linux/amd64 and linux/arm64 output without QEMU.

# golang:1.26.5-bookworm pins the latest confirmed 1.26.x patch on Debian
# bookworm as of this writing (verified against the Docker Hub registry
# tag list); avoids the "1.26-bookworm" floating tag drifting under CI.
FROM --platform=$BUILDPLATFORM golang:1.26.5-bookworm AS builder

WORKDIR /src

# Copy only the dependency manifests first so `go mod download` is cached
# as its own layer and is only invalidated when dependencies change, not
# on every source edit.
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

# CGO_ENABLED=0 keeps the binary static so it runs unmodified on the
# distroless runtime image, which has no libc shared objects to link
# against. -trimpath strips local build paths for reproducibility.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags "-s -w \
      -X github.com/showmeshsystems/showmesh/internal/version.Version=${VERSION} \
      -X github.com/showmeshsystems/showmesh/internal/version.Commit=${COMMIT} \
      -X github.com/showmeshsystems/showmesh/internal/version.BuildDate=${BUILD_DATE}" \
    -o /out/showmesh-coordinator \
    ./cmd/showmesh-coordinator

# The distroless runtime image has no shell, so there is no way to run
# `mkdir`/`chown` there. Pre-create the data directory here, in the
# builder stage (which does have a shell), and copy it across with
# --chown below. This is what makes a freshly created named Docker
# volume mounted at /var/lib/showmesh writable by the nonroot UID:GID
# 65532:65532 that the runtime image runs as; a directory copied in by
# COPY carries the ownership set here, but an empty volume Docker
# creates on first `docker run` would otherwise be root-owned.
RUN mkdir -p /out/data && chown -R 65532:65532 /out/data

# distroless/static has no shell, no package manager, and no libc beyond
# what's needed to run static binaries: minimal attack surface for an
# internet-facing-adjacent coordinator. The "nonroot" variant already
# runs as UID:GID 65532:65532.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/showmesh-coordinator /usr/local/bin/showmesh-coordinator
COPY --from=builder --chown=65532:65532 /out/data /var/lib/showmesh

# Apache-2.0 section 4(a) requires recipients receive a copy of the
# license; the distroless base carries only its own third-party licenses,
# so this repo's license text must be added explicitly.
COPY LICENSE /usr/share/doc/showmesh/LICENSE

USER 65532:65532

EXPOSE 8080

ENV SHOWMESH_DATA_DIR=/var/lib/showmesh

# distroless has no shell and no curl, so the container can't run a shell
# healthcheck command. The coordinator binary implements a `-healthcheck`
# flag that performs the HTTP GET against its own /healthz internally and
# exits 0/1, which HEALTHCHECK can invoke directly as an exec form.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/showmesh-coordinator", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/showmesh-coordinator"]

LABEL org.opencontainers.image.source="https://github.com/ShowMeshSystems/showmesh" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="showmesh-coordinator" \
      org.opencontainers.image.description="ShowMesh coordinator: orchestration and observation appliance for FPP/xLights/Resolume holiday light displays."
