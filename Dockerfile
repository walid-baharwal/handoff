FROM golang:1.24-alpine AS build

ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal

RUN mkdir -p /out /downloads \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X handoff/internal/handoff.Version=${VERSION}" -o /out/handoff ./cmd/handoff \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X handoff/internal/handoff.Version=${VERSION}" -o /downloads/handoff-linux-amd64 ./cmd/handoff \
    && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X handoff/internal/handoff.Version=${VERSION}" -o /downloads/handoff-linux-arm64 ./cmd/handoff \
    && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -X handoff/internal/handoff.Version=${VERSION}" -o /downloads/handoff-windows-amd64.exe ./cmd/handoff \
    && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w -X handoff/internal/handoff.Version=${VERSION}" -o /downloads/handoff-darwin-amd64 ./cmd/handoff \
    && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w -X handoff/internal/handoff.Version=${VERSION}" -o /downloads/handoff-darwin-arm64 ./cmd/handoff \
    && cd /downloads \
    && sha256sum handoff-* > SHA256SUMS

FROM alpine:3.22

RUN addgroup -S handoff \
    && adduser -S -G handoff handoff \
    && mkdir -p /data \
    && chown handoff:handoff /data
COPY --from=build /out/handoff /usr/local/bin/handoff
COPY --from=build /downloads /downloads

USER handoff
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/handoff"]
CMD ["serve"]
