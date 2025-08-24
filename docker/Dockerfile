# ===== BUILD =====
FROM golang:1.23.6-alpine AS builder
WORKDIR /mx-chain-proxy-go
RUN apk add --no-cache git build-base
COPY . .

# Build static-ish binary for Alpine (musl). No CGO to avoid libc issues.
WORKDIR /mx-chain-proxy-go/cmd/proxy
RUN CGO_ENABLED=0 GOOS=linux go build \
  -ldflags="-s -w -X main.appVersion=mainnet -X main.commitID=latest" \
  -o /proxy

# ===== RUNTIME =====
FROM alpine:3.20
# curl is needed for Coolify's container healthcheck probe; certs for HTTPS if your app needs it
RUN apk add --no-cache curl ca-certificates && update-ca-certificates

# Copy binary from builder
COPY --from=builder /proxy /usr/local/bin/proxy

# (Optional) drop root
# RUN adduser -D -H app && chown app:app /usr/local/bin/proxy
# USER app

WORKDIR /app
EXPOSE 8080

ENTRYPOINT ["proxy","--start-swagger-ui"]
