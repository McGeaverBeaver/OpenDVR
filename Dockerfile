# ---- web client -----------------------------------------------------------
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- backend ----------------------------------------------------------------
FROM golang:1.26-trixie AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/opendvr ./cmd/server

# ---- comskip ----------------------------------------------------------------
# Debian 13 dropped the comskip package, so it is built here from Debian's
# source (pinned by hash) plus a small FFmpeg 7 API fix. It is linked against
# the libav* that jellyfin-ffmpeg already ships (FFmpeg 7.1, same sonames),
# so the runtime carries no second, distro copy of FFmpeg.
FROM debian:trixie-slim AS comskip
ARG COMSKIP_VERSION=0.82.009+git20230112.d0cc422+ds.1
ARG COMSKIP_DEBIAN_REVISION=2.1
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates curl xz-utils patch build-essential autoconf automake libtool pkg-config \
        libargtable2-dev libavcodec-dev libavformat-dev libavutil-dev libswscale-dev
COPY docker/comskip-ffmpeg7.patch /tmp/comskip-ffmpeg7.patch
RUN set -eux; \
    cd /tmp; \
    orig="comskip_${COMSKIP_VERSION}.orig.tar.xz"; \
    deb="comskip_${COMSKIP_VERSION}-${COMSKIP_DEBIAN_REVISION}.debian.tar.xz"; \
    # pool first; snapshot.debian.org keeps the files if Debian drops them.
    curl -fsSL -o "$orig" "https://deb.debian.org/debian/pool/main/c/comskip/$orig" \
      || curl -fsSL -o "$orig" https://snapshot.debian.org/file/e403a3018fcb4b3c576f8d699f801f6aecb683d6; \
    curl -fsSL -o "$deb" "https://deb.debian.org/debian/pool/main/c/comskip/$deb" \
      || curl -fsSL -o "$deb" https://snapshot.debian.org/file/4da340e382cac391053aac03ebc448d3eac48815; \
    printf '%s  %s\n' \
        c01ad7d27cbc48b14ecc3b3e64c77af287375a0f2bd6e99c295a5d16a9139447 "$orig" \
        af1195afd0f9797b947f74f708eef37edd24a51e106900b87fdcd17ceba6bafd "$deb" | sha256sum -c -; \
    mkdir src; \
    tar -xJf "$orig" -C src --strip-components=1; \
    tar -xJf "$deb" -C src; \
    cd src; \
    for p in $(cat debian/patches/series); do patch -p1 < "debian/patches/$p"; done; \
    patch -p1 < /tmp/comskip-ffmpeg7.patch; \
    autoreconf -fi; \
    ./configure LDFLAGS="-Wl,-rpath,/usr/lib/jellyfin-ffmpeg/lib"; \
    make -j"$(nproc)"; \
    strip comskip; \
    install -D -m 0755 comskip /out/comskip

# ---- runtime ----------------------------------------------------------------
# Debian 13 + jellyfin-ffmpeg 7.1 + comskip (the DVR's commercial detector).
# jellyfin-ffmpeg bundles libva, the Intel iHD driver, gmmlib and oneVPL, and
# the server points LIBVA_DRIVERS_PATH at them (stream.ConfigureDriverPath),
# so Quick Sync/VAAPI need nothing more from Debian. The container needs
# /dev/dri passed through for hardware encoding.
FROM debian:trixie-slim AS runtime

ENV DEBIAN_FRONTEND=noninteractive
ADD --checksum=sha256:a0cde241ae297fa6f0265c0bf15ce9eb9ee97c008904a59ab367a67d59532839 \
    https://repo.jellyfin.org/jellyfin_team.gpg.key /usr/share/keyrings/jellyfin.asc
COPY --from=comskip /out/comskip /usr/bin/comskip
RUN set -eux; \
    chmod 0644 /usr/share/keyrings/jellyfin.asc; \
    apt-get update; \
    apt-get -y upgrade; \
    apt-get install -y --no-install-recommends ca-certificates; \
    printf '%s\n' 'Types: deb' 'URIs: https://repo.jellyfin.org/debian' 'Suites: trixie' \
        'Components: main' 'Architectures: amd64' 'Signed-By: /usr/share/keyrings/jellyfin.asc' \
        > /etc/apt/sources.list.d/jellyfin.sources; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        jellyfin-ffmpeg7 \
        libargtable2-0 \
        tzdata \
        iputils-ping \
        util-linux; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*; \
    # comskip must resolve every library (libav* come from jellyfin-ffmpeg).
    if ldd /usr/bin/comskip | grep 'not found'; then exit 1; fi

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
