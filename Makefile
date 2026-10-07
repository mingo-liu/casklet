GO ?= go
GOOS ?= linux
GOARCH ?= $(shell $(GO) env GOARCH)
ROOTFS ?= rootfs/busybox
PREFIX ?= /usr/local
DESTDIR ?=

.PHONY: build install fmt fmt-check vet test test-race rootfs test-integration

build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -o bin/mdocker ./cmd/mini-docker
	ln -sf mdocker bin/mini-docker

install:
	test "$$(uname -s)" = Linux
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
	./scripts/prepare-rootfs.sh "$(ROOTFS)"

test-integration:
	$(MAKE) build GOOS=linux GOARCH=$$($(GO) env GOHOSTARCH)
	MINI_DOCKER_ROOTFS="$(abspath $(ROOTFS))" ./scripts/test-linux.sh
