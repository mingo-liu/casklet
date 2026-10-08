# mini-docker

A macOS command-line container tool with its own Go container engine. `mdocker`
automatically manages a dedicated Lima Linux VM; containers run in that VM using
mini-docker's namespaces, cgroups v2, and process supervision. Docker Engine and
containerd are not required.

Supported hosts: macOS 13.5+ on Apple silicon or Intel, with Lima 2.0+.
The Linux executable is an internal guest component, not a separately supported
host product. Use trusted workloads; this learning runtime does not provide a
security guarantee for untrusted code.

## Quick start

```sh
brew install lima
make build
sudo make install

mdocker doctor
mdocker run -- /bin/sh -c 'hostname; ps; echo hello'
mdocker run -it -- /bin/sh
```

You can use `./bin/mdocker` without installing. Run it as your regular Mac user,
without sudo. The first command that needs the engine creates the
`mini-docker-runtime` VM, installs the bundled engine, and prepares a static
BusyBox filesystem. Initial setup needs internet access; subsequent runs use
the existing VM and its local data. Container options precede `--` when supplying a command. Registry images provide
their own default command and are downloaded automatically on a cache miss.

## Run an application image

```sh
mdocker run -d --name redis --image redis:8
# Publish to localhost on your Mac:
mdocker run -d --name redis-web --network bridge -p 127.0.0.1:6379:6379 --image redis:8
```

OCI/Docker images include programs, dependencies, and startup defaults. The guest
pulls the matching Linux architecture, verifies and unpacks its layers, and runs
it with mini-docker's engine. Cached images work offline. Use `mdocker image pull
redis:8` to refresh a tag, or a registry digest to pin its source. Supply application
settings with `--env`, persistent directories with `--mount`, and command arguments
after `--`. See the [image guide](guides/usage.md#ocidocker-images).

## Common operations

```sh
mdocker run -d --name worker --memory 128m --pids-limit 64 --cpus 0.5 \
  -- /bin/sleep 300
mdocker ps
mdocker exec worker -- /bin/sh -c 'hostname; id'
mdocker logs --tail 20 worker
mdocker inspect worker
mdocker stats worker
mdocker stop worker
mdocker rm worker
```

Your Mac home directory is shared with the VM for rootfs imports and bind mounts.
Containers and imported images remain on the VM's Linux disk. Published TCP and
UDP ports are forwarded back to the Mac. The default network is loopback only;
use `--network bridge` for connectivity and port publishing.

```sh
mdocker run --mount "type=bind,source=$PWD,target=/work" --workdir /work \
  -- /bin/sh -c 'ls'
mdocker machine status
mdocker machine stop
mdocker machine start
```

The VM defaults to 4 CPUs, 4 GiB memory, and a 20 GiB disk. Before its first use,
you can choose resources and additional shared directories:

```sh
mdocker machine init --cpus 2 --memory 2 --disk 20 --mount /Volumes/Projects
```

## Documentation and development

- [Usage guide](guides/usage.md): files, images, networking, security, and lifecycle.
- [Development guide](guides/development.md): builds, tests, and troubleshooting.
- [Architecture](ARCHITECTURE.md): host/guest boundaries and resource ownership.
- `mdocker help`: command overview; `mdocker COMMAND --help`: syntax, options, and examples.

```sh
make fmt-check test vet test-race vuln
make test-macos # Real commands through the runtime VM; creates it if needed.
```

The internal Linux engine retains a privileged integration suite in a separate
dedicated development VM. Generated binaries, templates, runtime state, and
local `docs/` notes remain untracked. See [LICENSE](LICENSE).
