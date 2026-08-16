# dora

[![English](https://img.shields.io/badge/Language-English-blue.svg)](README.md)
[![简体中文](https://img.shields.io/badge/语言-简体中文-red.svg)](README.zh-CN.md)

`dora` is a tiny, modular LLM agent kernel for Go. Its core is one loop and
two interfaces: `Model` and `Tool`.

No configuration file is required for the built-in providers: set the selected
provider's API key (for example `DEEPSEEK_API_KEY`), optionally select a profile
with `DORA_MODEL=provider/profile`, and run immediately.

See [`docs/architecture.md`](docs/architecture.md) for module boundaries,
dependencies, interfaces, and runtime flows.

[DoraBar](https://github.com/lgxz/DoraBar) is a companion macOS tray/menu-bar
app for dora, written in Swift. It provides a lightweight menu-bar interface
for interacting with the dora CLI.

## Benchmark Results

dora is evaluated on [terminal-bench-2.1](https://github.com/harbor-framework/terminal-bench-2.1)
(89 tasks) using the `deepseek-v4-flash` model. The table below tracks the
mean pass rate across evaluation runs as dora evolves:

| Run | Date | Mean | Passed | Notes |
|-----|------|------|--------|-------|
| 1 | 2026-08-12 | 0.573 | 51/89 | Baseline |
| 2 | 2026-08-13 | 0.483 | 43/89 | Rate-limit affected |
| 3 | 2026-08-13 | 0.596 | 53/89 |  |
| 4 | 2026-08-14 | 0.607 | 54/89 | Background job support |
| 5 | 2026-08-14 | 0.618 | 55/89 | System prompt |
| 6 | 2026-08-14 | 0.685 | 61/89 | read/write/edit tools |

## Scope

The kernel supports optional model streaming events while keeping its baseline
`Model` interface synchronous. Tool calls are deliberately executed only after
the current model response completes, and multiple tool calls in one response
are executed concurrently (results are emitted in the returned order). There is
no built-in memory, policy engine, middleware, or provider SDK.

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

To replace an unmanaged or development build (for example, one installed with
`make install`) with the latest release, bypassing the standalone-install
marker and version checks:

```sh
dora -update --force
```

Usage:

```sh
cat AGENTS.md | dora Summarize the following content | mdcat 
```

### Build from source

Building Dora requires Go 1.25 or newer. CI checks both the minimum supported
Go 1.25 line and the current Go 1.26 line; release binaries use the latest Go
1.26 patch release.

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
commit, and build date, writing to `dist/dora` (`dist/dora.exe` on Windows).
Override `VERSION`, `COMMIT`, or `BUILD_DATE` when needed. On Windows the
equivalent Go command can be run directly with `dist/dora.exe` as the output
path when `make` is unavailable.

To build a release binary and install it into `$(PREFIX)/bin/dora` (default
`$HOME/.local/bin/dora`), use:

```sh
make install
```

`install` depends on `release` and creates `$(PREFIX)/bin` if needed. Override
`PREFIX` to choose another location, for example `make install PREFIX=/usr/local`.

### API keys

Dora derives an API key environment variable from each provider name: uppercase
the name, replace non-alphanumeric characters with `_`, and append `_API_KEY`.
For example, `deepseek`, `trust`, and `open-router` use
`DEEPSEEK_API_KEY`, `TRUST_API_KEY`, and `OPEN_ROUTER_API_KEY`. A non-empty
process value overrides the same key under config `env`. Config values are
internal fallbacks and are never exported to child processes.
`DORA_MODEL=provider/profile` selects both catalog entries and overrides
`client.provider` and `client.profile`. It splits at the first `/`, so the
profile portion may itself contain `/`.

macOS / Linux, temporary (current terminal only):

```sh
export TRUST_API_KEY="sk-..."
export DORA_MODEL="trust/deepseek-v4-flash"
```

macOS / Linux, permanent: append the `export` line above to `~/.zshrc` (zsh)
or `~/.bashrc` (bash), then reload it:

```sh
source ~/.zshrc
```

Windows PowerShell, temporary (current session only):

```powershell
$env:TRUST_API_KEY = "sk-..."
$env:DORA_MODEL = "trust/deepseek-v4-flash"
```

Windows PowerShell, permanent (persists for the current user):

```powershell
[Environment]::SetEnvironmentVariable("TRUST_API_KEY", "sk-...", "User")
[Environment]::SetEnvironmentVariable("DORA_MODEL", "trust/deepseek-v4-flash", "User")
```

Windows CMD, temporary (current session only):

```cmd
set TRUST_API_KEY=sk-...
set DORA_MODEL=trust/deepseek-v4-flash
```

`DORA_MODEL` must contain non-empty provider and profile names. Leave it unset
or empty to use the `client` selector. Without either selector, Dora selects the
only provider with a non-empty API key. Multiple keyed providers are ambiguous;
with no keys, only a sole provider is selected automatically. The selected
provider's first model profile is the default. With no configuration file and
no keys, Dora retains the built-in DeepSeek default.

To customize the defaults, create `~/.dora/config.yaml`. Dora uses this
`~/.dora/` layout on every operating system, including macOS. Set the
`DORA_HOME` environment variable to an absolute path to override the home
directory. You can also place the file anywhere and pass
`--config path/to/config.yaml`; an explicitly requested file must exist.

```yaml
env:
  DEEPSEEK_API_KEY: sk-...
```

This minimal file uses the embedded DeepSeek catalog, auto-selects DeepSeek as
the sole keyed provider, and uses its first profile. Replacing the key with
`TRUST_API_KEY` selects Trust. When both keys are configured, add `client` or
set `DORA_MODEL` explicitly:

```yaml
env:
  DEEPSEEK_API_KEY: sk-deepseek...
  TRUST_API_KEY: sk-trust...
client:
  provider: trust
  profile: deepseek-v4-flash
```

The embedded provider catalog supplies built-in `deepseek` and `trust`
definitions with `base_url`, so catalog entries with those names may omit that
field. Models are always listed explicitly in `providers[].models`. Each
entry's `name` is a unique profile name used by `client.profile`; `model` is
the identifier sent to the provider. `model` defaults to `name` when omitted,
and multiple profiles may use the same model with different parameters. Set
provider-level or model-level `api: responses` to use the Responses API. Both
APIs always use SSE streaming.
Responses tool loops replay typed output items locally and do not depend on
server-side response storage.

### Third-party OpenAI-compatible providers

To use any third-party provider that speaks the OpenAI Chat Completions
protocol (for example Ollama, LM Studio, vLLM, Groq, Together, OpenRouter, or
a self-hosted endpoint), add it to `providers` with `base_url` and models. The Chat Completions endpoint is
`base_url + "/chat/completions"`, so `base_url` should be the provider's `/v1`
(or equivalent) root.

For a self-hosted Ollama endpoint that requires no authentication, leave
`OLLAMA_API_KEY` unset:

```yaml
providers:
  - name: ollama
    base_url: http://localhost:11434/v1
    models:
      - name: llama3.1
```

For a hosted OpenAI-compatible service that requires a key, such as OpenRouter
or Groq, export `OPENROUTER_API_KEY` or `GROQ_API_KEY` respectively, or put the
same name under config `env`:

```yaml
providers:
  - name: openrouter
    base_url: https://openrouter.ai/api/v1
    models:
      - name: openrouter/auto
env:
  OPENROUTER_API_KEY: sk-...
```

Leave both the real variable and config fallback absent for a local endpoint
that does not require authentication. Provider names that normalize to the
same environment variable are rejected. Config files containing keys are
secrets and should be protected accordingly.

Control the per-response output budget and sampling with `max_tokens` and
`temperature`:

```yaml
providers:
  - name: openrouter
    base_url: https://openrouter.ai/api/v1
    models:
      - name: balanced
        model: openrouter/auto
        max_tokens: 32768
        temperature: 0.7
client:
  provider: openrouter
  profile: balanced
```

`max_tokens` caps the number of tokens the model generates in one response and
defaults to 32768. It is sent on the wire as `max_tokens` for the
`chat_completions` API and as `max_output_tokens` for the `responses` API; an
explicit `0` means "no explicit cap" and is relayed as-is. `temperature` has no
default: when it is omitted, no value is sent and the provider uses its default
sampling. It accepts values in `[0, 2]`. Because some reasoning and
tool-calling models ignore or reject non-default temperatures, treat
`temperature` as best-effort. Both keys are model catalog settings; there are
no command-line flags for them.

`context_window` belongs to a model profile and approximates that model's
context capacity using message-content bytes because Dora does not currently
tokenize requests. It defaults to 1048576 (1 MiB), and configured values must
be positive. This estimate does not include exact tokenization, tool schemas,
or vision tokens.

`thinking` controls the model's "thinking mode" reasoning effort. Set it to one
of `off`, `minimal`, `low`, `medium`, or `high`. It has no default: when
omitted, no value is sent and the provider uses its own reasoning default.
Support varies by provider, and unsupported values are silently ignored rather
than causing an error:

- **openai**: on the Responses API all of `off`→`none`, `minimal`, `low`,
  `medium`, and `high` are sent; on Chat Completions `minimal`–`high` are sent
  as `reasoning_effort`, but `off` is not supported (gpt-5's floor is
  `minimal`) and is ignored.
- **deepseek**: `low`/`medium`/`high` are sent on both APIs, `minimal` is not
  supported and is ignored, and `off` is sent as `thinking.type: disabled` on
  Chat Completions or `reasoning.effort: none` on Responses.
- **trust**: treated best-effort like OpenAI on both APIs.

Because a setting may simply be dropped, treat `thinking` as a hint rather
than a guarantee. For one invocation, `--thinking` overrides the selected
model's setting with one of `off`, `minimal`, `low`, `medium`, or `high`:

```sh
./dora --thinking high "Solve a hard problem"
```

Dora runs up to 256 model-tool rounds per segment by default. Keep the safeguard
but adjust it for unusually long tool workflows when needed:

```yaml
agent:
  max_rounds: 96
```

Override it for one invocation with `--max-rounds`:

```sh
./dora --max-rounds 96 "Complete a long task"
```

When the limit is reached with both stdin and stderr attached to a terminal,
Dora asks whether to continue for another segment. Confirming resumes from the
completed tool output without replaying work. Declining stops normally without
persisting the incomplete turn. With piped or redirected I/O, Dora does not
prompt and returns `dora: maximum rounds exceeded` instead.

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

When stdout is a terminal, Dora prints the final answer as plain text.
Redirected or piped stdout is identical, preserving stable output for scripts:

```sh
./dora "Write release notes"
./dora "Write release notes" > release-notes.md
```

Colors are enabled automatically for terminal output. Set `NO_COLOR=1` to keep
the layout without ANSI colors; progress remains visible on stderr.

### Sessions

Pass a SQLite file to retain completed turns across CLI invocations:

```sh
./dora -s ./system-status.sqlite "Analyze this machine's system status"
./dora -s ./system-status.sqlite "Continue with the busiest processes"
```

Every invocation is a fresh, independent turn. Previous messages are never
loaded into the model context automatically. When the selected session database
already contains completed turns, Dora adds a `history` tool: the model can
`list` completed turns, see each turn's round count, and `get` chronological
round pages using `turn_id`, `offset`, and `limit`. An empty database does not
expose the tool. A round is one assistant tool-call message plus all
corresponding tool result messages. Only a successfully completed turn is
appended atomically; provider continuation is kept only while that turn runs.

The SQLite database contains `turns` and `messages` tables and records the
system prompt, user input, final result, backend metadata, and intermediate
tool rounds. Newly created files use `0600` permissions. The old named JSON
session format, `--fresh`, and automatic migration are not supported. Omit
`--session`/`-s` for an ephemeral turn. Session databases can contain commands
and tool output, so treat them as sensitive.

Use `--config`, `--thinking`, `--max-rounds`, or `--no-skills` to override the
corresponding configuration for one invocation.

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

By default, Dora discovers skills in `~/.dora/skills` (or `DORA_HOME/skills`)
followed by `~/.agents/skills/`, independent of the active `config.yaml` path.
Each default directory is included only if it exists as a directory (an absent
default is silently skipped). No configuration is needed.

Use `skills.directories` to replace the defaults with a specific set of parent
directories:

```yaml
skills:
  directories:
    - /absolute/path/to/additional-skills
```

Each configured path must be an absolute path or start with `~/`. Relative
paths are rejected. Configured directories are converted to their absolute
form and deduplicated; they are used exactly as listed and are not merged with
the defaults. Use `--no-skills` to disable every skill source for one
invocation; it takes precedence over `skills.directories`.

Dora advertises only skill names and descriptions in the `skill` tool schema.
The absolute skill directory and complete `SKILL.md` are returned when the
model calls that tool, allowing instructions to reference files such as
`scripts/check.sh`. The skill tool never executes those files; execution still
requires an enabled tool such as Bash. Names must contain lowercase letters,
numbers, and hyphens, and must match their directory name. Duplicate names are
rejected. A missing or empty default directory simply leaves the tool disabled;
malformed discovered skills and missing explicitly configured or command-line
directories are errors.

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
    timeout_seconds: 120
  powershell:
    enabled: true
    timeout_seconds: 120
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
or defaults to 120 seconds when that setting is zero or absent.

## Image understanding

Dora treats vision support as an intrinsic model-profile property. Set
`vision: true` on a catalog entry only when that model supports images; there
is no CLI override or direct image-attachment flag.

When a command tool's stdout contains a `@@path@@` tag, Dora parses it and
attaches the image at that path to the tool message for a vision-capable
profile. The command tool description documents this convention so the model
knows it can emit such a tag.

Images consume model context: each attached image is encoded into vision tokens
that count toward the model's context window, so large images or many images
can exhaust a small context window. Dora limits each image file to 4 MiB and
rejects files that are not images. When a `@@path@@` tag points at a missing,
non-image, or oversized file, Dora does not attach it and instead reports the
error to the model so it can correct the path.
