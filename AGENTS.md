# Repository Guidelines

## Project Structure & Module Organization

This directory currently contains no application code, tests, assets, or build configuration. It is not yet initialized as a Git repository. Update this guide as the project takes shape.

When adding the initial implementation, group source files by responsibility and document the entry point in `README.md`. Use the chosen language’s standard layout; keep tests alongside their modules or in a dedicated `tests/` directory. Place supporting scripts in `scripts/` and sample configuration in clearly named example files.

## Build, Test, and Development Commands

No build, test, or local development commands are currently configured. When introducing a toolchain, provide reproducible commands in `README.md` for installing dependencies, running locally, building, and testing. Explain prerequisites and required configuration. If a `Makefile` is added, consider consistent targets such as `make build`, `make test`, and `make lint`; these commands do not exist yet.

## Coding Style & Naming Conventions

Follow the selected language’s established formatting and naming conventions. Configure its formatter and linter when adding source code, and document their invocation. Keep indentation consistent within each file, use descriptive names, and avoid unrelated formatting changes.

## Testing Guidelines

No testing framework or coverage threshold is established. Add tests for new behavior and bug fixes using the selected framework’s naming conventions. Keep tests deterministic and document any external services they require. Run the relevant tests before submitting changes, and report any checks that could not run.

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
docs: define the mini-docker MVP implementation plan
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
