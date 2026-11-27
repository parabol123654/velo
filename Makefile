.PHONY: all build build-web build-go test test-web check race clean run

# The release tag, e.g. v0.3.0 (or v0.3.0-4-gabc1234 between tags); "dev" without git.
# Override with `make VERSION=v0.3.0`; an unrelated VERSION environment variable is ignored.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/parabol123654/velo/internal/version.Version=$(VERSION)

all: build

build-web:
	cd web && npm install --no-audit --no-fund && npm run build

build-go:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o velo ./cmd/velo

build: build-web build-go

test:
	CGO_ENABLED=0 go test ./... -v

test-web:
	cd web && npm test

# Every check a pull request needs, except race (it needs cgo and a C compiler).
check: build
	go vet ./...
	@unformatted="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$$unformatted" ]; then echo "Run gofmt -w on:"; echo "$$unformatted"; exit 1; fi
	CGO_ENABLED=0 go test ./...
	cd web && npm test

race: build-web
	CGO_ENABLED=1 go test -race ./...

run: build
	./velo serve --addr :8080 --data ./data

clean:
	rm -f velo velo.exe
	rm -rf web/node_modules
