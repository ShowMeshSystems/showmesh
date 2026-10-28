# ShowMesh preview gateway image (ADR-060).
#
# Build from the repository root:
#   docker build -f deploy/preview-gateway/Dockerfile .
#
# The NDI runtime (libndi.so.6) is never part of this image (ADR-010). The
# operator downloads it and mounts it at /opt/ndi at run time. The entrypoint
# starts D-Bus and avahi-daemon as root, which NDI discovery needs, then runs
# the gateway as the unprivileged showmesh user (uid 10001). tini is PID 1 so
# exited helper processes are reaped.

# CI replaces this stage with the named build context ndi-plugin, a directory
# holding <arch>/libgstndi.so built by the node agent's plugin jobs.
FROM debian:trixie AS ndi-plugin
ARG TARGETARCH
COPY deploy/node/ndi-plugin/build-ndi-plugin.sh /usr/local/bin/build-ndi-plugin.sh
RUN /usr/local/bin/build-ndi-plugin.sh /${TARGETARCH}

FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm AS gateway
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/showmesh-preview-gateway/ ./cmd/showmesh-preview-gateway/
COPY internal/previewgateway/ ./internal/previewgateway/
COPY internal/version/ ./internal/version/
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-X github.com/showmeshsystems/showmesh/internal/version.Version=${VERSION} \
                -X github.com/showmeshsystems/showmesh/internal/version.Commit=${COMMIT} \
                -X github.com/showmeshsystems/showmesh/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/showmesh-preview-gateway ./cmd/showmesh-preview-gateway

# go2rtc v1.9.14 was published 2026-01-19. The checksums are the sha256 of
# the release assets, equal to the digests GitHub reports for them.
FROM --platform=$BUILDPLATFORM debian:trixie-slim AS go2rtc
ARG TARGETARCH
ARG GO2RTC_VERSION=v1.9.14
ARG GO2RTC_SHA256_AMD64=32d616af226bd731678ffde328b94cfb94e30339bfefc469cfb76323144615a6
ARG GO2RTC_SHA256_ARM64=359fabade8a7a51e81a55fe6df6b0ef81764a5e1d63179577534eaaa71904b50
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
 && case "$TARGETARCH" in \
      amd64) sum="$GO2RTC_SHA256_AMD64" ;; \
      arm64) sum="$GO2RTC_SHA256_ARM64" ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && curl -fsSL -o /go2rtc "https://github.com/AlexxIT/go2rtc/releases/download/${GO2RTC_VERSION}/go2rtc_linux_${TARGETARCH}" \
 && echo "${sum}  /go2rtc" | sha256sum -c - \
 && chmod 0755 /go2rtc

FROM debian:trixie-slim
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ffmpeg \
      gstreamer1.0-tools gstreamer1.0-plugins-base gstreamer1.0-plugins-base-apps gstreamer1.0-plugins-good \
      gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly \
      dbus avahi-daemon libavahi-client3 tini \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --create-home --home-dir /home/showmesh showmesh

COPY --from=ndi-plugin /${TARGETARCH}/libgstndi.so /usr/local/lib/gstreamer-1.0/libgstndi.so
COPY --from=go2rtc /go2rtc /usr/local/bin/go2rtc
COPY --from=gateway /out/showmesh-preview-gateway /usr/local/bin/showmesh-preview-gateway
COPY deploy/preview-gateway/avahi-daemon.conf /etc/showmesh/avahi-daemon.conf
COPY deploy/preview-gateway/entrypoint.sh /usr/local/bin/entrypoint.sh

ENV GST_PLUGIN_PATH=/usr/local/lib/gstreamer-1.0 \
    GST_REGISTRY=/tmp/gst-registry.bin \
    SHOWMESH_PREVIEW_GATEWAY_ADDR=:8095

EXPOSE 8095 8555
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/showmesh-preview-gateway", "healthcheck"]
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["/usr/local/bin/showmesh-preview-gateway"]
