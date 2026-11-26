# Stage 1: Build Frontend SPA
FROM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Stage 2: Build Pure-Go Static Binary (No CGO)
FROM golang:1.26-alpine AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-builder /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/velo ./cmd/velo

# Stage 3: Minimal Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=go-builder /out/velo /usr/local/bin/velo
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["velo", "serve", "--addr", ":8080", "--data", "/data"]
