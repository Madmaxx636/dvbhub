# syntax=docker/dockerfile:1

# ---- build (runs natively and cross-compiles for the target platform) ----
FROM --platform=$BUILDPLATFORM golang:1 AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /dvbhub ./cmd/dvbhub

# ---- runtime ----
FROM debian:trixie-slim
# ffmpeg for transcoding (NVENC/NVDEC, VAAPI and Quick Sync are built in);
# VAAPI drivers for AMD/Intel GPUs and the Intel media runtime (x86 only).
# The NVIDIA container toolkit injects NVIDIA's driver libraries at run time.
RUN apt-get update \
 && extra="" \
 && if [ "$(dpkg --print-architecture)" = amd64 ]; then extra="intel-media-va-driver libmfx-gen1.2 libvpl2"; fi \
 && apt-get install -y --no-install-recommends ffmpeg mesa-va-drivers $extra \
      ca-certificates curl kmod pci.ids dtv-scan-tables \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /dvbhub /usr/local/bin/dvbhub

LABEL org.opencontainers.image.title="dvbhub" \
      org.opencontainers.image.description="TV tuner server for Jellyfin and Plex: scanning, guide, passthrough and GPU transcoding" \
      org.opencontainers.image.source="https://github.com/Madmaxx636/dvbhub"
ENV NVIDIA_VISIBLE_DEVICES=all \
    NVIDIA_DRIVER_CAPABILITIES=compute,video,utility
VOLUME /data
EXPOSE 9980
HEALTHCHECK --interval=30s --timeout=5s CMD curl -sf http://127.0.0.1:9980/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/dvbhub", "-data", "/data", "-listen", ":9980"]
