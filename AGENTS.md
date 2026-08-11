# Dora project guide

Dora is a small, modular LLM agent written in Go. The root `dora` package is
the provider-neutral agent kernel; the CLI assembles concrete model, tool,
storage, and presentation implementations around it.

Read [`docs/architecture.md`](docs/architecture.md) for the complete module
relationships, interfaces, runtime flow, persistence model, and current
limitations. Read [`README.md`](README.md) for user-facing setup and usage.

## Files and directories

### Agent kernel (`package dora`)

- `agent.go`: immutable Agent construction and the model/tool execution loop.
- `model.go`: `Model`, optional `StreamingModel`, and model request/response
  types.
- `tool.go`: `Tool`, `ToolSpec`, and `ToolCall`.
- `message.go`: provider-neutral conversation roles and messages.
- `observer.go`: semantic progress updates and the `Observer` interface.
- `result.go`: resumable `State` input and complete `Result` output.

The kernel contains no CLI, configuration, filesystem, HTTP protocol, or
terminal implementation.

### Executable and application assembly

- `cmd/dora/main.go`: process entry point, signal handling, terminal detection,
  and final error reporting.
- `internal/cli/`: argument and stdin parsing, configuration overrides,
  provider/tool construction, session lifecycle, and stdout/stderr routing.

`internal/cli` is the composition root and is the place where concrete
implementations are wired together.

### Model providers

- `model/openai/`: OpenAI-compatible Chat Completions adapter.
- `model/openairesponses/`: streaming Responses API adapter and opaque
  continuation handling.

Both packages translate their wire protocol to and from the types in the root
`dora` package.

### Tools and skills

- `tool/bash/`: Bash tool with timeout and output limits; CLI policy enables it
  automatically on Linux and macOS.
- `tool/powershell/`: independent PowerShell tool that prefers `pwsh` and falls
  back to `powershell.exe`; CLI policy enables it automatically on Windows.
- `skill/`: discovers and validates `SKILL.md` packages and exposes them through
  one on-demand `dora.Tool`.

### Internal services

- `internal/config/`: strict YAML decoding, environment lookup, and validation.
- `internal/paths/`: cross-platform XDG configuration, skill, and session paths.
- `internal/session/`: versioned JSON session snapshots, atomic replacement,
  and revision conflict detection.
- `internal/update/`: standalone-install provenance checks, GitHub release
  discovery, checksum validation, and rollback-capable executable replacement.
- `internal/progress/`: concise terminal rendering of `dora.Observer` updates.

### Documentation and examples

- `README.md`: installation, configuration, CLI, sessions, skills, and Bash
  usage.
- `docs/architecture.md`: authoritative architecture description.
- `config.example.yaml`: annotated configuration example.

Tests live beside the implementation as `*_test.go` files.

## Validation

Run the focused package test while editing, then run the full validation before
hand-off:

```sh
make check
make release
```

`make check` runs `go test ./...`, `go vet ./...`, `go test -race ./...`, and
`git diff --check`. `make release` verifies the stripped production build.

If package boundaries, interfaces, runtime flow, persistence, or paths change,
check whether `docs/architecture.md`, `README.md`, or `config.example.yaml` also
needs an update.

## Releasing

Releases are published from GitHub Actions. Pushing a semantic version tag
(`v*`) triggers the `Release` workflow, which runs `make check`, renders the
versioned installers, and uses GoReleaser to build and publish archives for
Linux, macOS, and Windows.

Before tagging, always run the full validation locally and confirm it is green:

```sh
make check
```

Release steps:

1. Ensure `main` is up to date with `origin/main` and the working tree is clean.
2. Tag the release commit: `git tag -a vX.Y.Z -m "Release vX.Y.Z"`.
3. Push the tag: `git push origin vX.Y.Z`. This triggers the release workflow.
4. Confirm the workflow completes successfully and the release assets appear on
   the GitHub Releases page.

If a release fails after the tag was already pushed:

1. Fix the underlying issue and commit it on `main`.
2. Push `main`, then delete and recreate the tag on the fixed commit:
   `git tag -d vX.Y.Z && git push origin :refs/tags/vX.Y.Z`
   `git tag -a vX.Y.Z -m "Release vX.Y.Z" && git push origin vX.Y.Z`.

Common pitfall: when changing tool descriptions or other observable behavior,
update the corresponding `*_test.go` assertions in the same change. A stale
test that passes locally on one platform can still fail `make check` on the CI
runner (for example, a description that embeds `runtime.GOOS`/`runtime.GOARCH`
differs between macOS and Linux).
