# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/admin ./cmd/admin

FROM alpine:latest
RUN addgroup -S -g 10001 app && adduser -S -D -H -u 10001 -G app app
COPY --from=builder /out/server /usr/local/bin/server
COPY --from=builder /out/admin /usr/local/bin/admin
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["server"]

