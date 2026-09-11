# Pomona's server: one static binary with the pages compiled in.
#
# Build from the repo root. The embed lives in the root package, so the
# whole tree is the build context and `./server` is the main package.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY assets.go ./
COPY src ./src
COPY icons ./icons
COPY server ./server
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pomona ./server

# alpine rather than scratch: TLS roots for Slack, GitHub and Anthropic, and
# the timezone database for "07:00 in the reader's timezone".
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S pomona && adduser -S -G pomona -h /data pomona \
 && mkdir -p /data && chown pomona:pomona /data
COPY --from=build /pomona /usr/local/bin/pomona
USER pomona
VOLUME /data
EXPOSE 7777
ENV POMONA_ADDR=0.0.0.0:7777 POMONA_DATA=/data
ENTRYPOINT ["pomona"]
