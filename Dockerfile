# Stage 1: Build Frontend SPA (on the build machine: the output is the same for every platform)
FROM --platform=$BUILDPLATFORM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Stage 2: Build Pure-Go Static Binary (No CGO), cross-compiled for the target platform
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS go-builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-builder /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X github.com/parabol123654/velo/internal/version.Version=${VERSION}" \
    -o /out/velo ./cmd/velo

# Stage 3: Minimal Runtime
FROM alpine:3.21
LABEL org.opencontainers.image.source=https://github.com/parabol123654/velo
LABEL org.opencontainers.image.description="Velo: a self-hosted issue tracker for small teams and their coding agents"
LABEL org.opencontainers.image.licenses=MIT
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=go-builder /out/velo /usr/local/bin/velo
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/api/health || exit 1
ENTRYPOINT ["velo", "serve", "--addr", ":8080", "--data", "/data"]
