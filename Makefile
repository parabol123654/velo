.PHONY: all build build-web build-go test clean run

all: build

build-web:
	cd web && npm install --no-audit --no-fund && npm run build

build-go:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o velo ./cmd/velo

build: build-web build-go

test:
	CGO_ENABLED=0 go test ./... -v

run: build
	./velo serve --addr :8080 --data ./data

clean:
	rm -f velo velo.exe
	rm -rf web/node_modules
