# casklet

[English](README.md) · [简体中文](README.zh-CN.md)

A macOS command-line container tool with its own Go engine. casklet automatically
manages a dedicated Lima Linux VM and runs containers using Linux namespaces,
cgroups v2, and process supervision. No Docker Engine or containerd required.

Requires **macOS 13.5+** (Apple silicon or Intel) and **Lima 2.0+**. Linux is an
internal guest component, not a separately supported host platform. This is a
learning runtime: use trusted workloads; it provides no security guarantee for
untrusted code.

## Install

```sh
brew install lima
```

Download the matching archive and `SHA256SUMS` from
[Releases](https://github.com/mingo-liu/casklet/releases) into an empty directory.
Set the downloaded version below; use `arm64` for Apple silicon or `amd64` for
Intel. Continue only if the checksum reports `OK`:

```sh
CASKLET_VERSION=v0.1.0
CASKLET_ARCH=arm64
shasum -a 256 --ignore-missing -c SHA256SUMS &&
  tar -xzf "casklet_${CASKLET_VERSION}_darwin_${CASKLET_ARCH}.tar.gz" &&
  sudo install -d /usr/local/bin &&
  sudo install -m 0755 casklet /usr/local/bin/casklet
casklet --version
```

The archive includes the client with its matching Linux engine, both READMEs,
usage/development guides, architecture documentation, and the license. Go and Make
are unnecessary.
Release binaries are not Developer ID signed or notarized, so macOS may require
approval in **System Settings > Privacy & Security** after a browser download.

Alternatively, build from the repository directory with **Go 1.27.2+** and Make:

```sh
make build
sudo make install
```

You can also run `./bin/casklet` without installing. To update, repeat your chosen
installation method. The bundled guest engine updates automatically on the next
engine command, preserving containers and images.

## Quick start

Run casklet as your regular Mac user, **without sudo**:

```sh
casklet doctor
casklet run -- /bin/sh -c 'hostname; ps; echo hello'
casklet run -it -- /bin/sh
```

The first engine command creates the `casklet-runtime` VM and prepares the engine
and a BusyBox filesystem. Initial setup needs internet access; subsequent runs
reuse the VM and local data. If setup fails, restore connectivity and retry.

Run an application and publish its port to localhost on your Mac:

```sh
casklet run -d --name redis --network bridge -p 127.0.0.1:6379:6379 --image redis:8
casklet ps
casklet logs --tail 20 redis
casklet exec -it redis -- /bin/sh
```

Use `exit` to leave the exec shell; Redis keeps running. OCI/Docker images are
pulled automatically for the native Linux architecture and cached for offline
use. `image pull` refreshes cached tags. Put explicit command arguments after
`--`; omit the command and separator to use image startup defaults.

The default network is loopback only. Use `--network bridge` for connectivity
and TCP/UDP port publishing, or a named network for isolated service discovery.
OCI images do not currently support rootless execution.

## Manage containers and data

```sh
casklet inspect redis
casklet stats redis
casklet stop redis
casklet start redis
casklet restart redis
casklet stop redis
casklet rm redis
```

`inspect` reports exit reasons (including OOM and timeout) and preserves the
previous result after restart. Commands accept container names, full IDs, or unique
ID prefixes. Stop/start and restart preserve private files; `rm` deletes them.
Bind mounts and named volumes
preserve data independently of containers. Your Mac home directory is shared
with the VM; other directories need an explicit share.

```sh
casklet run --mount "type=bind,source=$PWD,target=/work" --workdir /work -- /bin/ls
casklet volume create app-data
casklet run --mount type=volume,source=app-data,target=/data -- /bin/sh -c 'echo saved > /data/message'
```

Deploy multiple services from a [project configuration](guides/usage.md#project-deployment)
with networks, volumes, and startup dependencies:

```sh
casklet project validate --config project.json
casklet project up --config project.json --wait --timeout 1m
casklet project ps demo
casklet project down demo
```

Preview deployment reasons and changed field names with `project up --config
project.json --dry-run`; applying changed services requires `--recreate SERVICE`.
Use the guide's `demo` configuration for this example. `project down` preserves
data volumes; `--volumes` also removes project-owned volumes.

Use `project logs --follow demo` for service-prefixed output.

## Capabilities

See the [usage guide](guides/usage.md) for full examples, defaults, and limits.

| Area | Supported workflows |
| --- | --- |
| Images | [OCI/Docker pulls and local directory imports](guides/usage.md#ocidocker-images), [private registries with macOS Keychain credentials](guides/usage.md#registry-credentials), [offline OCI archives](guides/usage.md#offline-oci-image-archives) |
| Execution | [CPU, memory, PID and temporary-storage limits, environment, users, read-only roots](guides/usage.md#resource-and-execution-options), [interactive run/exec](guides/usage.md#terminals-and-exec) |
| Lifecycle | [Logs and diagnostics](guides/usage.md#background-lifecycle-and-diagnostics), [restart policies](guides/usage.md#automatic-restarts), [health checks](guides/usage.md#container-health-checks), [live resource monitoring](guides/usage.md#multi-container-resource-monitoring) |
| Networking | [TCP/UDP port publishing](guides/usage.md#ipv4-networking), [isolated named networks and service discovery](guides/usage.md#named-networks-and-service-discovery) |
| Storage | [Bind mounts and named volumes](guides/usage.md#persistent-data), [volume backup and migration](guides/usage.md#volume-backup-restore-and-migration), [disk usage and image cleanup](guides/usage.md#disk-usage-and-cache-cleanup), [download cache limits](guides/usage.md#shared-download-cache) |
| Deployment | [JSON configuration and env-files](guides/usage.md#repeatable-deployment-configuration), [labels and batch operations](guides/usage.md#labels-filters-and-batch-lifecycle-operations), [multi-service projects and selective replacement](guides/usage.md#project-deployment) |
| Isolation | [Security controls and rootless restrictions](guides/usage.md#security-and-rootless-execution) |

## Virtual machine

The VM defaults to **4 CPUs, 4 GiB memory, and a 20 GiB disk**. Containers, images,
and volumes live on its disk. Check or stop it with:

```sh
casklet machine status
casklet machine stop
casklet machine start
```

Stopping the VM stops workloads but preserves data. On boot, `always` and eligible
`unless-stopped` containers resume automatically; start other retained containers
with `casklet start NAME`.

Use `machine init` before first use to customize resources and shares, or
`machine resize` on a stopped VM to adjust resources; disks can only grow.
See [VM configuration](guides/usage.md#macos-execution-environment).

## Documentation and development

- `casklet help` lists commands; `casklet COMMAND --help` shows options and examples.
- [Usage guide](guides/usage.md): detailed workflows and limitations.
- [Development guide](guides/development.md): builds, tests, troubleshooting, and releases.
- [Architecture](ARCHITECTURE.md): module boundaries and resource ownership.

```sh
make fmt-check test vet test-race
make test-macos
```

Privileged Linux tests share the `casklet-runtime` VM and require the
[data-preserving exclusive test workflow](guides/development.md#internal-linux-engine-tests).
Include `casklet version --json` when reporting a problem; it works without
Lima or a running VM.

See [LICENSE](LICENSE).
