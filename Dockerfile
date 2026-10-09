# syntax=docker/dockerfile:1.7

# ---- web client -----------------------------------------------------------
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- backend ----------------------------------------------------------------
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/opendvr ./cmd/server

# ---- runtime ----------------------------------------------------------------
# Debian + jellyfin-ffmpeg + comskip (the DVR's commercial detector).
# Jellyfin's ffmpeg bundles the Intel oneVPL runtime,
# so Quick Sync works with only the iHD media driver added. The container needs
# /dev/dri passed through for hardware encoding.
FROM debian:bookworm-slim AS runtime

ENV DEBIAN_FRONTEND=noninteractive
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl gnupg tzdata; \
    curl -fsSL https://repo.jellyfin.org/jellyfin_team.gpg.key | gpg --dearmor -o /usr/share/keyrings/jellyfin.gpg; \
    echo "deb [signed-by=/usr/share/keyrings/jellyfin.gpg arch=amd64] https://repo.jellyfin.org/debian bookworm main" > /etc/apt/sources.list.d/jellyfin.list; \
    sed -i 's/ main$/ main contrib non-free non-free-firmware/' /etc/apt/sources.list.d/debian.sources; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        jellyfin-ffmpeg7 \
        comskip \
        intel-media-va-driver-non-free \
        libva-drm2 \
        libigdgmm12 \
        iputils-ping \
        iproute2 \
        procps \
        util-linux; \
    apt-get purge -y gnupg; \
    apt-get autoremove -y; \
    rm -rf /var/lib/apt/lists/*

COPY --from=build /out/opendvr /app/opendvr
COPY docker/entrypoint.sh /app/entrypoint.sh
RUN chmod 0755 /app/entrypoint.sh
COPY --from=web /src/web/dist /app/web
# The Android TV app, when CI built it (see .github/workflows/docker.yml).
COPY apk/ /app/apk/

ENV LISTEN_ADDR=:8089 \
    DATA_DIR=/config \
    BUFFER_DIR=/buffer \
    RECORDINGS_DIR=/recordings \
    WEB_DIR=/app/web \
    APK_DIR=/app/apk \
    FFMPEG_PATH=/usr/lib/jellyfin-ffmpeg/ffmpeg \
    ENCODER=auto \
    RENDER_DEVICE=/dev/dri/renderD128 \
    LIBVA_DRIVER_NAME=iHD

VOLUME ["/config", "/buffer", "/recordings"]
EXPOSE 8089

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD ["/app/opendvr", "-healthcheck"]

# Starts as root to fix folder ownership, then drops to PUID:PGID.
ENV PUID=99 \
    PGID=100 \
    UMASK=022

ENTRYPOINT ["/app/entrypoint.sh"]
