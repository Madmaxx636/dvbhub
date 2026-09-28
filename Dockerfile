# syntax=docker/dockerfile:1

# ---- build (runs natively and cross-compiles for the target platform) ----
FROM --platform=$BUILDPLATFORM golang:1-bookworm AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /dvbhub ./cmd/dvbhub

# ---- runtime ----
FROM debian:bookworm-slim
# Debian's ffmpeg includes NVENC/NVDEC support; the NVIDIA container runtime
# injects the driver libraries (libnvidia-encode, libcuda) and nvidia-smi.
# If the CUDA filters (scale_cuda/yadif_cuda) are missing, dvbhub falls back
# to CPU deinterlace/scale automatically and still encodes with NVENC.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg ca-certificates curl kmod pci.ids \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /dvbhub /usr/local/bin/dvbhub

LABEL org.opencontainers.image.title="dvbhub" \
      org.opencontainers.image.description="DVB tuner server for Jellyfin/Plex with NVENC transcoding"
ENV NVIDIA_VISIBLE_DEVICES=all \
    NVIDIA_DRIVER_CAPABILITIES=compute,video,utility
VOLUME /data
EXPOSE 9980
HEALTHCHECK --interval=30s --timeout=5s CMD curl -sf http://127.0.0.1:9980/discover.json >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/dvbhub", "-data", "/data", "-listen", ":9980"]
