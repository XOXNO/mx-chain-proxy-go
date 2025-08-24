FROM golang:1.23.6 AS builder
LABEL maintainer="multiversx"

WORKDIR /mx-chain-proxy-go
COPY . .

# Proxy
WORKDIR /mx-chain-proxy-go/cmd/proxy
RUN go build -ldflags="-X main.appVersion=mainnet -X main.commitID=latest"

# ===== SECOND STAGE ======
FROM ubuntu:latest
RUN apt-get update -y && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /mx-chain-proxy-go/cmd/proxy /mx-chain-proxy-go/cmd/proxy

WORKDIR /mx-chain-proxy-go/cmd/proxy/
EXPOSE 8080
ENTRYPOINT ["./proxy", "--start-swagger-ui"]
