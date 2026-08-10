# dora

`dora` is a tiny, modular LLM agent kernel for Go. Its core is one loop and
two interfaces: `Model` and `Tool`.

See [`docs/architecture.md`](docs/architecture.md) for module boundaries,
dependencies, interfaces, and runtime flows.

```go
model := newMyModel()
weather := newWeatherTool()

agent, err := dora.New(model, weather)
if err != nil {
	log.Fatal(err)
}

result, err := agent.Run(ctx, []dora.Message{
	{Role: dora.RoleUser, Content: "What's the weather?"},
})
if err != nil {
	log.Fatal(err)
}

fmt.Println(result.Content)
```

The agent is stateless. Keep `result.Messages` and pass them to a later call to
continue a conversation.

## Scope

The kernel supports optional model streaming events while keeping its baseline
`Model` interface synchronous. Tool calls are deliberately executed only after
the current model response completes, and are still sequential. There is no
built-in memory, policy engine, middleware, or provider SDK.

## CLI

### Install

On macOS or Linux, install the latest release with curl:

```sh
curl -LsSf https://github.com/lgxz/dora/releases/latest/download/dora-installer.sh | sh
```

Use wget when curl is unavailable:

```sh
wget -qO- https://github.com/lgxz/dora/releases/latest/download/dora-installer.sh | sh
```

On Windows, use PowerShell:

```powershell
powershell -ExecutionPolicy Bypass -c "irm https://github.com/lgxz/dora/releases/latest/download/dora-installer.ps1 | iex"
```

The installers download the archive for the current OS and architecture,
verify it against the release SHA-256 checksums, and install `dora` into
`$HOME/.local/bin` by default. Set `DORA_INSTALL_DIR` to choose another
directory:

```sh
curl -LsSf https://github.com/lgxz/dora/releases/latest/download/dora-installer.sh \
  | env DORA_INSTALL_DIR=/usr/local/bin sh
```

Install a specific release by using its tagged installer URL. Each installer
is pinned to the release that contains it:

```sh
curl -LsSf https://github.com/lgxz/dora/releases/download/v0.1.0/dora-installer.sh | sh
```

Run `dora --version` to inspect the installed version, source commit, and build
date. Standalone releases that include self-update support can update themselves:

```sh
dora -update
```

The updater checks the latest stable GitHub Release, verifies its archive
against `checksums.txt`, validates the downloaded executable, and replaces the
current binary with rollback on failure. Go builds, manual archive installs,
and package-manager installs remain unmanaged; upgrade those through
their original installation method. Re-run the latest installer once to enable
self-update on an older installation. Release archives and checksums remain
available on the GitHub Releases page for manual installation and verification.

### Build from source

Build the command with debug information:

```sh
make build
```

The binary is written to `build/dora` (`build/dora.exe` on Windows). For a
smaller distribution binary without symbol and DWARF debug data, use:

```sh
make release
```

The release target uses `-trimpath`, strips debug data, and embeds the version,
commit, and build date. Override `VERSION`, `COMMIT`, or `BUILD_DATE` when
needed. On Windows the equivalent Go command can be run directly with
`build/dora.exe` as the output path when `make` is unavailable.

With `DEEPSEEK_API_KEY` set, Dora runs without a configuration file. It uses
the built-in `deepseek` provider defaults described below.

To customize the defaults, create
`${XDG_CONFIG_HOME:-$HOME/.config}/dora/config.yaml`. Dora uses this XDG layout
on every operating system, including macOS. You can also place the file
anywhere and pass `--config path/to/config.yaml`; an explicitly requested file
must exist.

```yaml
model:
  provider: deepseek
```

The `deepseek` preset defaults to the `chat_completions` API,
`deepseek-v4-flash`, `https://api.deepseek.com`, and `DEEPSEEK_API_KEY`. The
`openai` preset defaults to `chat_completions`, `gpt-5`,
`https://api.openai.com/v1`, and `OPENAI_API_KEY`. Override any preset field
when needed, and set `api: responses` to use the Responses API. Both APIs
always use SSE streaming. Responses tool loops replay typed output items
locally and do not depend on server-side response storage.

Literal `api_key` is also supported, but an environment variable keeps secrets
out of the configuration file. A non-empty literal key takes precedence over
`api_key_env`. Set `api_key_env: ""` explicitly for a local endpoint that does
not require authentication.

Dora allows up to 64 model turns per task by default. Keep the safeguard but
adjust it for unusually long tool workflows when needed:

```yaml
agent:
  max_model_calls: 96
```

Run a one-shot prompt or combine an instruction with piped input:

```sh
export DEEPSEEK_API_KEY="..."
./dora "Explain this repository"
git diff | ./dora "Review this change"
```

Progress is shown on stderr with a small Dora personality, while the final
answer remains on stdout. Use `--quiet` or `-q` when only the answer is wanted:

```sh
./dora --quiet "Explain this repository"
```

Colors are enabled automatically when stderr is a terminal. Set `NO_COLOR=1`
to keep progress visible without ANSI colors.

### Sessions

Use a session name to continue the same conversation across CLI invocations:

```sh
./dora -s system-status "Analyze this machine's system status"
./dora -s system-status "Continue with the busiest processes"
```

Start over under the same session name with `--fresh`. Existing history is
ignored for this run and replaced only after the new task succeeds; if the run
fails, the previous session remains intact:

```sh
./dora -s system-status --fresh "Analyze this machine from scratch"
```

Session names may contain letters, numbers, `.`, `_`, and `-`. Dora stores each
session as a versioned JSON snapshot with `0600` permissions. Session v3 binds
the configured provider, API, model, and base URL: Chat Completions resumes
from messages, while Responses additionally persists its opaque typed-item
continuation. Use `--fresh` before changing a session's backend. Version 1 and
2 session files are not supported. The default directory is
`${XDG_STATE_HOME:-$HOME/.local/state}/dora/sessions` on every operating
system. Omit `--session`/`-s` to keep the existing stateless behavior. Session
files can contain commands and tool output, so treat them as sensitive. Do not
run two Dora processes against the same session name concurrently.

Use `--config`, `--model`, `--base-url`, `--skills-dir`, or `--no-skills` to
override the corresponding configuration for one invocation.

### Skills

Skills are local instruction packages loaded by the model only when relevant.
Each skill is a directory containing a `SKILL.md` with strict YAML front
matter:

```text
skills/
└── system-status/
    └── SKILL.md
```

```markdown
---
name: system-status
description: Analyze CPU, memory, disk, and busy processes.
---

# System status

Inspect the machine methodically and summarize actionable findings.
```

By default, Dora discovers the `skills` directory beside the active
`config.yaml`. With the default config path, that is
`${XDG_CONFIG_HOME:-$HOME/.config}/dora/skills`. No configuration is needed.

Use `skills.directories` only to add more parent directories:

```yaml
skills:
  directories:
    - /absolute/path/to/additional-skills
```

For a one-off run, add one or more parent directories on the command line:

```sh
dora --skills-dir ./project-skills --skills-dir ~/shared-skills "Run checks"
```

Command-line directories are merged with the default and configured
directories, converted to absolute paths, and deduplicated. Use `--no-skills`
to disable every skill source for one invocation; it takes precedence over
both `--skills-dir` and `skills.directories`.

Dora advertises only skill names and descriptions in the `skill` tool schema.
The absolute skill directory and complete `SKILL.md` are returned when the
model calls that tool, allowing instructions to reference files such as
`scripts/check.sh`. The skill tool never executes those files; execution still
requires an enabled tool such as Bash. Names must contain lowercase letters,
numbers, and hyphens, and must match their directory name. Duplicate names are
rejected. A missing or empty default directory simply leaves the tool disabled;
malformed discovered skills and missing explicitly configured or command-line
directories are errors.

### Migrating macOS paths

Dora does not automatically read or migrate the previous macOS directory.
Move existing files manually before running the new version:

```sh
mkdir -p "$HOME/.config/dora" "$HOME/.local/state/dora"
mv "$HOME/Library/Application Support/dora/config.yaml" "$HOME/.config/dora/config.yaml"
mv "$HOME/Library/Application Support/dora/skills" "$HOME/.config/dora/skills"
mv "$HOME/Library/Application Support/dora/sessions" "$HOME/.local/state/dora/sessions"
```

Skip any `mv` command whose source does not exist. If an XDG environment
variable is set, use its directory instead of the fallback destination shown
above.

## Releasing

Pushing a semantic version tag runs the release workflow:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The workflow runs the full validation, renders installers pinned to the tag,
and uses GoReleaser to publish static archives for Linux, macOS, and Windows on
amd64 and arm64. It also publishes `checksums.txt`; public repositories receive
GitHub build provenance attestations. Tags that do not match semantic version
syntax fail before publishing.

### Command tools

Command tools use platform-aware automatic defaults: Bash is enabled on Linux
and macOS, while PowerShell is enabled on Windows. The other command tool is
disabled even if its executable is on `PATH`. Omit `enabled` to use that policy,
or set it explicitly to override the platform default:

```yaml
tools:
  bash:
    enabled: false
    timeout_seconds: 30
  powershell:
    enabled: true
    timeout_seconds: 30
```

Automatic tools whose executable is absent are skipped. A tool explicitly
enabled with `enabled: true` must exist on `PATH`, otherwise Dora reports an
error. Discovery currently checks executable presence only; it does not launch
the shell to probe its runtime environment.

The Bash tool runs `bash -lc` in Dora's current directory. The model can use
`cd` inside a command when it needs another directory. The tool returns exit
code, stdout, stderr, timeout, and truncation information to the model as JSON.
Output is limited to 1 MiB per stream. This tool grants the model the same
filesystem and process permissions as the `dora` process, so disable it unless
you trust the environment in which Dora runs.

The independent `powershell` tool prefers PowerShell Core (`pwsh`) and falls
back to Windows PowerShell (`powershell.exe`). If both tools are explicitly
enabled, they are exposed separately so their command syntaxes remain distinct.

PowerShell also starts in Dora's current directory and can use `Set-Location`
inside a command when needed.

Both command tools accept an optional per-command timeout. It overrides the
configured default for that call and cannot exceed 3600 seconds:

```json
{
  "command": "go build ./...",
  "timeout_seconds": 300
}
```

When omitted, `timeout_seconds` comes from the corresponding YAML tool setting,
or defaults to 30 seconds when that setting is zero or absent.
