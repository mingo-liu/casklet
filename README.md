# casklet

[English](README.md) · [简体中文](README.zh-CN.md)

A macOS command-line container tool with its own Go container engine. `casklet`
automatically manages a dedicated Lima Linux VM; containers run in that VM using
casklet's namespaces, cgroups v2, and process supervision. Docker Engine and
containerd are not required.

Supported hosts: macOS 13.5+ on Apple silicon or Intel, with Lima 2.0+.
The Linux executable is an internal guest component, not a separately supported
host product. Use trusted workloads; this learning runtime does not provide a
security guarantee for untrusted code.

Use `casklet --version` or `casklet version --json` to report the version, source
commit, modified state, Go version, client platform and bundled Linux engine
SHA-256. The query runs locally without Lima or starting the VM. Include its
output when reporting a problem.

## Quick start

Building from source requires Go 1.27.2+ and Make. Run these commands from the
repository directory:

```sh
brew install lima
make build
sudo make install

casklet doctor
casklet run -- /bin/sh -c 'hostname; ps; echo hello'
casklet run -it -- /bin/sh
```

You can use `./bin/casklet` without installing. Run it as your regular Mac user,
without sudo. The first command that needs the engine creates the
`casklet-runtime` VM, installs the bundled engine, and prepares a static
BusyBox filesystem. Initial setup needs internet access; subsequent runs use
the existing VM and its local data. Container options precede `--` when supplying
a command. Registry images provide their own default command and are downloaded
automatically on a cache miss; `--` is unnecessary when using that default.

If initial dependency installation fails, restore network access and retry
`casklet machine start` or your original command. The client resumes incomplete
setup even when the VM is already running, preserving existing workloads and data.

After updating the source, run `make build` and `sudo make install` again to update
the installed client. Its bundled guest engine updates automatically on the next
engine command, preserving existing containers and images.

Use the same `casklet-runtime` VM for development and tests. Optional guest build
tools and the data-preserving, exclusive Linux test workflow are described in the
[development guide](guides/development.md#internal-linux-engine-tests).

## Run an application image

```sh
casklet run -d --name redis --image redis:8
# Publish to localhost on your Mac:
casklet run -d --name redis-local --network bridge -p 127.0.0.1:6379:6379 --image redis:8
```

OCI/Docker images include programs, dependencies, and startup defaults. The guest
pulls the matching Linux architecture, verifies and unpacks its layers, and runs
it with casklet's engine. Cached images work offline. Supply application
settings with `--env`, persistent directories with `--mount`, and command arguments
after `--`. Arguments replace image `Cmd` and retain `Entrypoint`; use `--entrypoint`
to override it. Anonymous and private registries are supported. Save a registry
password or access token with `casklet login --username USER --password-stdin REGISTRY`;
credentials stay in macOS Keychain. `casklet logout REGISTRY` removes them without
removing cached images. Login saves credentials; access is checked on pull. OCI
rootless execution is not currently supported. See the
[image guide](guides/usage.md#ocidocker-images).

For example, read a token from a protected file without putting it in arguments:

```sh
casklet login --username alice --password-stdin ghcr.io < /path/to/registry-token
casklet image pull ghcr.io/example/app:latest
casklet logout ghcr.io
```

Detached containers inherit image `Healthcheck` metadata; `--health-cmd` overrides
the probe, and `--no-healthcheck` disables it. `ps` and `inspect` show health separately
from process status. Probe failures do not trigger automatic restart. See
[health checks](guides/usage.md#container-health-checks) for timing and readiness.
Use `casklet wait --healthy --timeout 30s redis` before running dependent commands.
Health waits default to 30s (`0s` disables the deadline), return 124 on timeout,
and leave the workload running. They fail if the observed execution stops or changes.

Application settings can also come from `run --config redis.json` and repeatable
`--env-file application.env` on `run` or `exec`. JSON uses long option names; CLI
scalars override the file, and explicit `--env` overrides env-files. Files are read
on the Mac. Relative rootfs, bind sources, and env-file paths in JSON use the
configuration file directory; volume sources remain names. Complete execution
configurations, including inherited values and exec overrides, share a 1 MiB
serialized JSON limit; OS argument limits may be lower. See
[deployment configuration](guides/usage.md#repeatable-deployment-configuration).

Group detached containers with immutable labels, then filter and manage them in
batches. Labels survive restart and are independent of workload environment;
`ps --json` and `inspect` expose them. Repeated filters use AND; `ps -a` includes
stopped records. `stop`/`rm` accept multiple references or `--filter`/`--all`,
without mixing references and selectors. Removal requires stopped containers;
partial failures return 125 and preserve completed IDs on stdout. See
[labels and batch operations](guides/usage.md#labels-filters-and-batch-lifecycle-operations).

```sh
casklet run -d --name demo-worker --label project=demo -- /bin/sleep 300
casklet ps -a --filter label=project=demo --json
casklet stop --filter label=project=demo
casklet rm --filter label=project=demo --filter status=exited
```

## Pull progress and image management

Automatic pulls and explicit `image pull` show per-layer download, verification,
and extraction progress on stderr. `--progress=auto` (default) uses dynamic bars
in a terminal and plain text when redirected; `plain` and `tty` force either mode.
Image/container IDs and application output stay on stdout. Cached runs show
`Using cached image`; an unchanged explicit pull shows `Image is up to date`.

New privileged image containers share the immutable image through OverlayFS
and save only their writable changes. Detached changes survive start/restart;
directory sources, rootless runs, and existing copied container roots keep their
copy behavior. The guest must support OverlayFS. Different image pulls and
imports can prepare concurrently; cached-image operations remain available while
another image downloads. Refreshes of the same reference are serialized.
Layer downloads use up to three workers per pull and share a verified compressed
blob cache across images. Layers are applied in their original order; reused
layers show `Already exists`. The cache does not deduplicate unpacked files across
different images.
Image removal and abandoned transaction recovery also clean up large trees
outside the global metadata lock, keeping unrelated image operations available.
Active leases, retained container references, and mounted storage still prevent
deletion; interrupted cleanup is recovered by later imports, pulls, or removals.

```sh
casklet image pull redis:8
casklet image pull --progress=plain redis:8
casklet image ls
casklet image ls --json
```

The table shows image names and tags in `IMAGE`, 12-character IDs, and decimal
sizes such as `202MB` or `1.85GB`. Each reference has its own row; images without
references show `<none>`. `--json` retains full IDs, canonical references, and exact byte sizes.

Tags keep their cached version until `image pull` refreshes them. Use a registry
digest to pin the source. To delete an image, replace `IMAGE_ID` below with a full
local ID or unique hexadecimal prefix from `image ls` (such as its 12-character ID).
Ambiguous prefixes fail; use a longer ID from `image ls --json`.
Stop and remove all referencing containers first. Image removal accepts local
IDs/prefixes rather than registry names such as `redis:8`.

```sh
casklet image rm IMAGE_ID
```

Standard OCI layout tar archives can deliver native Linux applications offline:

```sh
image_id=$(casklet image load < app.tar)
casklet run --image "$image_id"
casklet image save "$image_id" > app-copy.tar
```

Load validates one native image and preserves its startup defaults and numeric
ownership without accessing registries or changing cached tags. Save synthesizes
one flattened OCI layer, preserving the local image ID when loaded again but
producing a new manifest digest. It accepts OCI images, including loaded images;
directory imports cannot be saved because their ownership and capability policy
differs. Save is limited to a 4 GiB layer including tar metadata. Load allows
32 GiB archives, 4 GiB per unpacked layer and 16 GiB total. Docker save archives
and compressed outer tar archives are unsupported. See [archive limits and
semantics](guides/usage.md#offline-oci-image-archives).

## Deploy a project

Use `project up --config project.json` to deploy multiple services with declared
networks, data volumes and `started`/`healthy` startup dependencies. Matching services
are reused; reordering network aliases keeps service identity, including after an
upgrade. Changed unselected configuration fails before deployment mutations. Resources use
`cp-PROJECT-LOGICAL` names and scoped service aliases. See the
[project configuration example](guides/usage.md#project-deployment).

```sh
casklet project up --config project.json --wait --timeout 1m
casklet project ps demo
casklet project down demo
# Also delete project-owned data volumes:
casklet project down --volumes demo
```

Preview and apply a selected service replacement with
`casklet project up --config project.json --recreate api --dry-run`, then remove
`--dry-run` to apply. Repeat `--recreate` for multiple services. Unchanged running
services and persistent volumes keep their identities/data; selected private
roots and logs are replaced. Selected services stop during replacement, and
removed old containers are not automatically restored on failure. Retry creates
missing services. Preview requires a running VM and does not pull images or
change deployments. Refresh a tag with `image pull` before recreating its service.

`--wait` requires all healthchecked services to be healthy and other services to
be running. Its timeout covers the whole deployment after parsing; a deadline
returns 124 after bounded cleanup. Increase it for cold VM/image preparation.

Failures clean only this invocation's newly created verified resources. Existing
unselected services and pre-existing volumes remain; `down` preserves data volumes by default. Local
ownership receipts prevent adopting or deleting unrelated same-name resources.

For restored or migrated data, declare `"external-volumes": {"data": "restored-data"}`
instead of listing `data` in `volumes`; mounts still refer to logical `data`.
Restore/create the guest volume first. Explicit external declarations release old
ownership receipts for those names; external volumes survive rollback and
`down --volumes`. Managed and external volumes share the 16-volume limit.

## Common operations

```sh
casklet run -d --name worker --memory 128m --pids-limit 64 --cpus 0.5 \
  -- /bin/sleep 300
casklet ps
casklet exec worker -- /bin/sh -c 'hostname; id'
casklet exec -it worker -- /bin/sh
casklet logs --tail 20 worker
casklet inspect worker
casklet stats worker
casklet stop worker
casklet start worker
casklet restart worker
casklet stop worker
casklet rm worker
```

Configure temporary storage with `casklet run --tmp-size 32m --shm-size 96m
--memory 256m -- /bin/sleep 300`. `/tmp` defaults to 16 MiB and `/dev/shm` to
64 MiB. Each accepts positive bytes or binary `k`/`m`/`g` units up to `128g`;
Linux rounds capacities to pages. Sizes also work as string fields in run JSON
and project service `run` objects, and persist across start/restart while their
contents are cleared. `inspect` reports configured byte capacities with defaults.
Allocated pages count against `--memory`; sizes do not reserve memory.

Monitor several containers or a labeled project with `casklet stats --watch worker database`
or `casklet stats --watch --filter label=project=demo`. Add `--json` for one JSON frame
per line; selection pins existing IDs and interruption leaves workloads running.
See [resource monitoring](guides/usage.md#multi-container-resource-monitoring).


`ps` and `stats` tables show the first 12 characters of container IDs. JSON output
and `inspect` retain full IDs. Management commands accept exact names, full IDs,
or unique hexadecimal ID prefixes of any length. Exact names take precedence;
ambiguous prefixes fail and require a longer ID or the exact name.

Use `exit` to leave the exec shell; the main container continues running.
Stop/start and restart preserve the container's private files; `rm` deletes them.
Bind-mounted host data survives container removal.

Your Mac home directory is shared with the VM for rootfs imports and bind mounts.
The image cache is at `/var/lib/casklet/images`, and container records and
private roots are at `/var/lib/casklet/containers`, inside the VM. Published
TCP and UDP ports are forwarded back to the Mac. The default network is loopback
only; use `--network bridge` for connectivity and port publishing.

```sh
casklet run --mount "type=bind,source=$PWD,target=/work" --workdir /work \
  -- /bin/sh -c 'ls'
casklet machine status
casklet machine stop
casklet machine start
```

Named networks isolate projects and resolve detached containers by name or alias:

```sh
casklet network create demo
casklet run -d --name demo-redis --network demo --network-alias redis --image redis:8
casklet run --network demo --image redis:8 -- redis-cli -h redis ping
casklet network ls
casklet stop demo-redis
casklet rm demo-redis
casklet network rm demo
```

Create networks before use. Network names, container names on named networks,
and aliases require lowercase DNS labels. Aliases are scoped per network and
reserved by stopped retained containers too. Discovery supports IPv4 over UDP
and TCP; unknown external names use upstream DNS. Direct traffic between named
networks and the legacy bridge is blocked. Published ports still reach the Mac.
Network metadata survives VM restarts; runtime interfaces and addresses are fresh
on each execution. See [named networks](guides/usage.md#named-networks-and-service-discovery).

Stopping the VM stops its workloads and preserves their data. After starting the
VM, `always` and eligible `unless-stopped` containers resume automatically;
use `casklet start NAME` for other retained containers.

The VM defaults to 4 CPUs, 4 GiB memory, and a 20 GiB disk. Before its first use,
you can choose resources and additional shared directories:

```sh
casklet machine init --cpus 2 --memory 2 --disk 20 --mount /Volumes/Projects
```

For an existing VM, stop it before changing resources. Omitted values stay unchanged;
disk size can only grow. The next start applies the configuration and expands the
existing disk while preserving containers, images, volumes, shares, and other settings.
`machine status` reports configured resources.

```sh
casklet machine stop
casklet machine resize --cpus 6 --memory 8 --disk 40
casklet machine start
```

Back up a stopped volume with `casklet volume export app-data > app-data.tar`,
then restore to a new name with `casklet volume restore app-restored < app-data.tar`.
The tar stream also supports migration to another Mac. See
[volume backups](guides/usage.md#volume-backup-restore-and-migration) for limits and consistency.

Named volumes keep data on the VM disk independently of containers:

```sh
casklet volume create app-data
casklet run --mount type=volume,source=app-data,target=/data -- /bin/sh -c 'echo saved > /data/message'
casklet volume ls
```

Volumes start empty with root ownership, survive container removal, and require
execution without user namespaces. `casklet volume rm app-data` deletes the data
only after all referencing containers are removed.

Inspect guest disk usage and preview or clean unused cached images:

```sh
casklet system df
casklet image prune --dry-run
casklet image prune
```

Pruning includes unused tagged images, preserves containers and volumes, and
refuses images with active leases or references from retained containers.
It also clears compressed download blobs not in use by a pull. `--dry-run` leaves
both images and blobs intact and prints eligible image IDs only. `system df`
reports unpacked image storage as `images` and download storage as `image-cache`.

Manage compressed downloads independently of retained images and containers:

```sh
casklet image cache ls --json
casklet image cache prune --dry-run
casklet image cache prune
casklet image cache limit 1g
```

The default limit is 2 GiB of retained compressed data; `limit 0` disables automatic
eviction. Settings persist in the VM. Setting a limit and completing a pull evict
least recently used idle blobs. Active leases can temporarily exceed the limit;
download staging, lock overhead, and unpacked images are excluded. Pulls still need
enough disk space for their downloads and extraction. Preview reports eligible
allocated bytes and blob/staging counts without deleting data.

Image `StopSignal` is retained for managed shutdown. Override it with
`--stop-signal SIGQUIT`; stop/restart uses that Linux signal before the configured
stop timeout forces termination. Foreground external signals remain unchanged.

Detached containers support automatic restarts:

```sh
casklet run -d --name service --restart unless-stopped -- /bin/sleep 300
casklet run -d --name retry-job --restart on-failure:3 -- /bin/sh -c 'exit 1'
```

The default is `no`. `on-failure[:1-1000]` retries nonzero or unknown exits;
`always` and `unless-stopped` also restart successful exits and resume after VM
boot. Manual stop suppresses restarts until start/restart; `always` resumes on the
next VM boot, while `unless-stopped` stays stopped. Retries back off from 1 to 30
seconds. Automatic published-port forwarding depends on Lima without a live Mac
port preflight. See the usage guide for retry counters and inspection fields.

## Documentation and development

- [Usage guide](guides/usage.md): files, images, networking, security, and lifecycle.
- [Development guide](guides/development.md): builds, tests, and troubleshooting.
- [Architecture](ARCHITECTURE.md): host/guest boundaries and resource ownership.
- `casklet help`: command overview; `casklet COMMAND --help`: syntax, options, and examples.

```sh
make fmt-check test vet test-race vuln
make test-macos # Real commands through the runtime VM; creates it if needed.
```

The internal Linux engine retains a privileged integration suite in the shared
`casklet-runtime` VM, with daily data preserved during exclusive testing.
Maintainers can enable scheduled/manual macOS end-to-end CI on dedicated Apple
silicon and Intel Mac runners; each run removes its disposable VM. Release
publication requires both architectures and hosted checks to pass before an
existing draft can be published. Missing configuration blocks publication. See the
[release gate](guides/development.md#release-publication-gate) and
[runner setup](guides/development.md#dedicated-macos-end-to-end-ci).
Generated binaries, templates, runtime state, and
local `docs/` notes remain untracked. See [LICENSE](LICENSE).
