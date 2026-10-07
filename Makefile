GO ?= go
GOOS ?= darwin
GOARCH ?= $(shell $(GO) env GOARCH)
ROOTFS ?= rootfs/busybox
PREFIX ?= /usr/local
DESTDIR ?=

.PHONY: build engine install fmt fmt-check vet test test-race rootfs test-integration

build:
	test "$(GOOS)" = darwin
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) $(GO) build -trimpath -o internal/machine/assets/mdocker-engine ./cmd/mini-docker
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -o bin/mdocker ./cmd/mini-docker
	ln -sf mdocker bin/mini-docker

# Internal guest engine, also used by the privileged Linux test suite.
engine:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) $(GO) build -trimpath -o bin/mdocker-engine ./cmd/mini-docker

install:
	test "$$(uname -s)" = Darwin
	test -x bin/mdocker
	install -d "$(DESTDIR)$(PREFIX)/bin"
	install -m 0755 bin/mdocker "$(DESTDIR)$(PREFIX)/bin/mdocker"

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

rootfs:
	@if [ "$$(uname -s)" = Darwin ]; then ./bin/mdocker rootfs "$(ROOTFS)"; else ./scripts/prepare-rootfs.sh "$(ROOTFS)"; fi

test-integration:
	$(MAKE) engine GOARCH=$$($(GO) env GOHOSTARCH)
	MINI_DOCKER_ROOTFS="$(abspath $(ROOTFS))" ./scripts/test-linux.sh
