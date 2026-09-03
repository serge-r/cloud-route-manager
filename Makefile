BINARY      := cloud-route-manager
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)
IMAGE       ?= ghcr.io/serge-r/$(BINARY)
PLATFORMS   ?= linux/amd64,linux/arm64

GOLANGCI_LINT_VERSION ?= latest

.PHONY: all build test race cover fmt vet lint tools tidy run check-config packages snapshot docker docker-multiarch clean

all: fmt vet test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY) .

test:
	go test ./...

race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

fmt:
	gofmt -w .

vet:
	go vet ./...

lint:
	golangci-lint run --timeout 5m

## tools: install golangci-lint built with the toolchain from go.mod; the
## released binaries refuse to analyse a module targeting a newer Go.
tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

tidy:
	go mod tidy

## run: single cycle against config.yml without touching the cloud
run: build
	./dist/$(BINARY) -config config.yml -once -dry-run -log-severity debug

check-config: build
	./dist/$(BINARY) -config config.example.yml -check-config

## packages: build deb/rpm/tar.gz locally into dist/
packages: snapshot

snapshot:
	goreleaser release --snapshot --clean

docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) -t $(IMAGE):$(VERSION) .

docker-multiarch:
	docker buildx build --platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) -t $(IMAGE):$(VERSION) .

clean:
	rm -rf dist coverage.out coverage.html
