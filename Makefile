GO ?= go
GOOS ?= linux
GOARCH ?= $(shell $(GO) env GOARCH)
ROOTFS ?= rootfs/busybox

.PHONY: build fmt vet test rootfs test-integration

build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -o bin/mini-docker ./cmd/mini-docker

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

rootfs:
	./scripts/prepare-rootfs.sh "$(ROOTFS)"

test-integration: build
	MINI_DOCKER_ROOTFS="$(abspath $(ROOTFS))" ./scripts/test-linux.sh
