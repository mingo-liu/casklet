# Repository Guidelines

## Project Structure & Module Organization

`cmd/mini-docker/` contains the executable entry point. `internal/cli` owns parsing and presentation; `internal/container` owns durable lifecycle management; `internal/runtime` owns process supervision and isolation. `internal/template` resolves rootfs/image sources and owns image leases, while `internal/rootfs`, `internal/image`, `internal/cgroup`, `internal/network`, and `internal/ipc` provide resource operations. See `ARCHITECTURE.md` for module boundaries and ownership. Unit tests live beside their packages; privileged Linux tests and bounded helpers live in `tests/integration/`. Development scripts are in `scripts/`, and the Lima configuration is in `dev/`.

`README.md` is the quick start; maintained usage and development documentation lives in `guides/`. `docs/` contains local design notes and is excluded from Git. Do not force-add its contents. Keep generated `bin/`, `rootfs/`, and runtime state untracked.

## Build, Test, and Development Commands

Use `make build` to compile the static Linux binary, `make rootfs` to generate a BusyBox template, and `make test` / `make vet` for unit tests and static checks. Run `make test-integration` inside the dedicated Linux VM to verify real isolation and resource limits. Use `mdocker` after `make build` and `sudo make install`; it automatically obtains privileges and a delegated systemd scope when needed. `scripts/run-linux.sh` remains a compatibility entry point. See `README.md` for VM setup and examples.

## Coding Style & Naming Conventions

Use English for repository documentation, identifiers, comments, CLI help, logs, errors, and commit descriptions. Discussions with the user remain in Chinese. Format Go code with gofmt (`make fmt`), follow standard Go naming conventions, and use `//go:build linux` for platform-specific code. Keep unsupported-platform errors explicit.

## Testing Guidelines

Use Go's `testing` package, `*_test.go` files, and `TestXxx` functions. Test behavior and failure cleanup rather than mirroring implementation. Keep resource stress helpers bounded. Run relevant unit tests and vet before committing; runtime changes also require privileged Linux integration tests. Report checks that could not run. No coverage percentage is mandated.

## Commit & Pull Request Guidelines

Use Conventional Commits for this project. These rules are adapted from [vela's commit guidelines](https://github.com/mingo-liu/vela/blob/main/AGENTS.md).

### Message Format

```text
<type>[optional scope][!]: <short summary>

<optional body>

<optional footer>
```

- A type is required. If a scope is useful, write it in parentheses, such as `(runtime)` or `(rootfs)`.
- Write all commit messages in English.
- Keep the subject short, specific, and imperative. Describe what the commit changes.
- For complex changes, leave a blank line after the subject and explain the reason and key effects in the body.
- Omit `!` unless the commit introduces a breaking change. Mark breaking changes with `!` after the type or scope, or add a `BREAKING CHANGE: <description>` footer.

### Commit Types

| Type | Purpose |
| --- | --- |
| `feat` | Add a feature |
| `fix` | Fix a bug |
| `docs` | Change documentation only |
| `refactor` | Restructure code without changing behavior |
| `test` | Add or update tests |
| `build` | Change build tooling or dependencies |
| `ci` | Change continuous integration configuration |
| `chore` | Perform other maintenance |

### Examples

```text
docs: describe the mini-docker runtime architecture
feat(runtime): add PID and mount namespace isolation
fix(rootfs): clean up temporary files after startup failure
test(cgroup): verify memory and process limits
build: add static Linux binary targets
feat(cli)!: require a separator before the container command
```

A breaking-change footer can explain the required migration:

```text
feat(cli)!: require a separator before the container command

Separate runtime options from command arguments to remove parsing ambiguity.

BREAKING CHANGE: Add -- before the command passed to mini-docker run.
```

### Pull Requests

Pull requests should explain the change, its purpose, and validation performed. Link relevant issues and include screenshots for visible interface changes.

## Security & Configuration

Never commit credentials or local secrets. Document required settings with sanitized examples and exclude generated artifacts and local configuration through `.gitignore` when those files are introduced.
