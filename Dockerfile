# syntax=docker/dockerfile:1
FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/admin ./cmd/admin

FROM alpine:latest@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
ARG VERSION=dev
ARG SOURCE=https://github.com/Cloud-Exit/ExitSkills
LABEL org.opencontainers.image.source="$SOURCE" \
      org.opencontainers.image.version="$VERSION"
ENV GOMEMLIMIT=384MiB
RUN addgroup -S -g 10001 app && adduser -S -D -H -u 10001 -G app app && \
    mkdir -p /data && chown 10001:10001 /data
COPY --from=builder /out/server /usr/local/bin/server
COPY --from=builder /out/admin /usr/local/bin/admin
USER 10001:10001
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["server"]
