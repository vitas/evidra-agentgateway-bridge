FROM docker.io/library/golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /evidra-agentgateway ./cmd/bridge

FROM docker.io/library/alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
RUN addgroup -S evidra && adduser -S -G evidra -h /data evidra
COPY --from=builder /evidra-agentgateway /usr/local/bin/evidra-agentgateway
ENV EVIDRA_BRIDGE_OBSERVATIONS=/data/observations.jsonl
EXPOSE 4317 4318
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O - http://127.0.0.1:4318/healthz >/dev/null || exit 1
USER evidra
ENTRYPOINT ["/usr/local/bin/evidra-agentgateway"]
