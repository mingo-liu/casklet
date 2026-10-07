# Development guide

[Quick start](../README.md) · [Usage](usage.md) · [Architecture](../ARCHITECTURE.md)

## macOS client

Install Go 1.25+, Make, and Lima 2.0+ on macOS 13.5+:

```sh
brew install lima
make build
./bin/mdocker doctor
```

`make build` first cross-compiles the native-architecture Linux engine into an
ignored embedded asset, then builds the Darwin client with that engine bundled.
The resulting `bin/mdocker` is self-contained apart from Lima. Installing this
file also installs its embedded engine payload; the end user needs neither Go
nor a source checkout. `sudo make install` copies only the macOS executable.
Do not run the client itself with sudo.

Select `GOARCH=arm64` for Apple silicon or `GOARCH=amd64` for Intel. The guest
architecture must match the client. Build each release architecture separately;
`go build` alone does not regenerate the embedded engine after guest changes.

| Command | Purpose |
| --- | --- |
| `make build` | macOS executable with bundled Linux engine |
| `sudo make install` | Install the macOS executable in `/usr/local/bin` |
| `make rootfs` | Export the built-in BusyBox template to `rootfs/busybox` |
| `make fmt` / `make fmt-check` | Format / check Go sources |
| `make test vet test-race` | Unit tests, static checks, and race detection |
| `make test-macos` | Opt-in macOS end-to-end tests through Lima |
| `make engine` | Internal Linux executable for guest development |
| `make test-integration` | Privileged Linux engine suite, inside a dedicated VM |

The macOS suite checks VM stop/start as well as commands and terminals. It skips
the VM reboot test if another active container exists, preserving that workload.

The client maintains its initialization lock and generated VM configuration in
`~/Library/Application Support/mini-docker`. Lima owns the VM disk and SSH
configuration under its own instance directory. The product instance is
`mini-docker-runtime`; it does not adopt or modify `mini-docker`, the existing
development instance.

## Internal Linux engine tests

Linux remains an internal execution and test environment. The ordinary macOS
unit suite excludes Linux-specific tests. Runtime changes must also pass the
privileged suite in a dedicated Linux VM.

The existing `dev/lima.yaml` creates a plain Ubuntu development VM with Go and
engine prerequisites; its disabled sharing and forwarding intentionally differ
from the product VM. Set up and transfer a fresh snapshot:

```sh
limactl start --name mini-docker dev/lima.yaml
set -o pipefail
./scripts/snapshot.sh | limactl shell mini-docker sh -c \
  'source_dir=$(mktemp -d "$HOME/mini-docker-source.XXXXXXXX") &&
    tar -xzf - -C "$source_dir" && printf "Source directory: %s\n" "$source_dir"'
limactl shell mini-docker
# Change to the directory printed above.
make engine rootfs
make fmt-check test vet test-race
make test-integration
```

Snapshots include tracked and unignored source files, including the embedded
rootfs helper, but exclude generated engines and host state. Use a fresh snapshot
after changes. Engine tests need root, systemd, cgroups v2, namespaces, and network
administration privileges, and must not run on a shared production Linux host.
CI retains Linux engine checks and adds macOS builds with bundled engines.

## Troubleshooting

- `mdocker machine status` reports the product VM without creating or starting it.
- `mdocker doctor` checks the guest runtime with the default BusyBox template.
- If startup was interrupted, retry `mdocker machine start`; engine installation
  is repeatable and executable replacement preserves existing container storage.
- A directory outside your shared home must be configured with `machine init
  --mount` before VM creation. The client reports unavailable shares explicitly.
- Initialization flags apply only to a new machine. To change an existing VM,
  stop it, use Lima's configuration tools, then start it. Preserve the product's
  dedicated loopback forwarding rules and writable home share.
- Guest diagnostics are available through `limactl shell mini-docker-runtime`.
  VM image download and Ubuntu package installation require internet access.
- Report integration checks that could not run. Unit tests and cross-compilation
  alone do not establish terminal, networking, or resource-limit behavior.
