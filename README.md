# mini-docker

A small Go container runtime for Linux arm64 and amd64. It provides namespace
isolation, cgroups v2 resource limits, interactive terminals, background
containers, local images, bind mounts, and optional IPv4 bridge networking.

Use it to learn about containers and run trusted programs in a dedicated Linux
VM. It does not provide a security guarantee for untrusted code, image
registries, or managed volumes.

## Quick start

Requirements: Linux 6.8+, systemd, cgroups v2, Go 1.25+, and a statically linked
BusyBox. On macOS, first follow the [Lima VM setup](guides/development.md#macos-development-vm).

On a dedicated Ubuntu 24.04 VM with Go installed:

```sh
sudo apt-get update
sudo apt-get install -y busybox-static binutils make iproute2 nftables util-linux conntrack
make build rootfs
sudo make install

mdocker doctor --rootfs ./rootfs/busybox
mdocker run --rootfs ./rootfs/busybox -- /bin/sh -c 'hostname; ps; echo hello'
mdocker run -it --rootfs ./rootfs/busybox -- /bin/sh
```

`mdocker` automatically obtains privileges through `sudo` and creates a delegated
systemd scope when needed. You can also run `./bin/mdocker` without installing.
Container options precede the required `--`; arguments after it are executed
directly. Use `/bin/sh -c` for shell syntax.

## Common operations

```sh
mdocker run -d --name worker --rootfs ./rootfs/busybox \
  --memory 128m --pids-limit 64 --cpus 0.5 -- /bin/sleep 300
mdocker ps
mdocker exec worker -- /bin/sh -c 'hostname; id'
mdocker logs --tail 20 worker
mdocker inspect worker
mdocker stats worker
mdocker stop worker
mdocker rm worker
```

Defaults: loopback-only networking, memory `128m`, process/thread limit `64`,
unlimited CPU and command duration, and shutdown grace `5s`. Foreground runs
remove their private rootfs on exit; background containers retain it until `rm`.
Use bind mounts to keep host data, or `--network bridge` for external networking.

## Documentation and development

- [Usage guide](guides/usage.md): images, storage, networking, security, and lifecycle behavior.
- [Development guide](guides/development.md): VM setup, tests, and troubleshooting.
- [Architecture](ARCHITECTURE.md): module boundaries, resource ownership, and extension points.
- `mdocker help`: complete command syntax and options.

```sh
make fmt-check test vet test-race
make test-integration # Run inside the dedicated Linux VM.
```

Generated binaries, rootfs templates, runtime state, and local `docs/` notes are
untracked. See [LICENSE](LICENSE) for licensing.
