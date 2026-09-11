# Pomona's server: one static binary with the pages compiled in, plus
# Claude Code, so an account can bring its own subscription rather than an
# API key.
#
# Build from the repo root. The embed lives in the root package, so the
# whole tree is the build context and `./server` is the main package.

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY assets.go ./
COPY src ./src
COPY icons ./icons
COPY server ./server
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pomona ./server

# Debian rather than alpine: Claude Code's native build wants glibc. TLS
# roots for Slack, GitHub and Anthropic; the timezone database for "07:00
# in the reader's timezone".
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata curl \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --system pomona \
 && useradd --system --gid pomona --create-home --home-dir /home/pomona pomona \
 && mkdir -p /data && chown pomona:pomona /data

# Claude Code, installed as the service user so it runs with no privileges.
# Each account supplies its own credential at run time through the
# environment of its own CLI process; the image holds no login.
USER pomona
ENV HOME=/home/pomona \
    PATH=/home/pomona/.local/bin:/usr/local/bin:/usr/bin:/bin \
    DISABLE_AUTOUPDATER=1
RUN curl -fsSL https://claude.ai/install.sh | bash -s -- stable \
 && claude --version

COPY --from=build /pomona /usr/local/bin/pomona
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# Start as root only long enough to own the volume, then drop to pomona
# (see docker-entrypoint.sh). A platform mounts the volume as root, and a
# server that cannot write its data directory keeps every account in memory
# and loses them all on the next restart.
USER root
VOLUME /data
EXPOSE 7777
ENV POMONA_ADDR=0.0.0.0:7777 POMONA_DATA=/data
ENTRYPOINT ["docker-entrypoint.sh"]
