GO ?= go
GOOS ?= darwin
GOARCH ?= $(shell $(GO) env GOARCH)
ROOTFS ?= rootfs/busybox
PREFIX ?= /usr/local
DESTDIR ?=

.PHONY: build engine install fmt fmt-check vet test test-race vuln rootfs test-integration test-macos

build:
	test "$(GOOS)" = darwin
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) $(GO) build -trimpath -o internal/machine/assets/casklet-engine ./cmd/casklet
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -o bin/casklet ./cmd/casklet

# Internal guest engine, also used by the privileged Linux test suite.
engine:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) $(GO) build -trimpath -o bin/casklet-engine ./cmd/casklet

install:
	test "$$(uname -s)" = Darwin
	test -x bin/casklet
	install -d "$(DESTDIR)$(PREFIX)/bin"
	install -m 0755 bin/casklet "$(DESTDIR)$(PREFIX)/bin/casklet"

fmt:
	gofmt -w cmd internal tests

fmt-check:
	./scripts/check-format.sh

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vuln:
	GO="$(GO)" ./scripts/check-vulnerabilities.sh

test-macos: build
	test "$$(uname -s)" = Darwin
	./bin/casklet doctor
	CASKLET_MACOS_INTEGRATION=1 CASKLET_MACOS_BINARY="$(abspath bin/casklet)" $(GO) test -v -timeout 10m ./tests/macos

rootfs:
	@if [ "$$(uname -s)" = Darwin ]; then ./bin/casklet rootfs "$(ROOTFS)"; else ./scripts/prepare-rootfs.sh "$(ROOTFS)"; fi

test-integration:
	$(MAKE) engine GOARCH=$$($(GO) env GOHOSTARCH)
	CASKLET_ROOTFS="$(abspath $(ROOTFS))" ./scripts/test-linux.sh
