# dora

[![English](https://img.shields.io/badge/Language-English-blue.svg)](README.md)
[![简体中文](https://img.shields.io/badge/语言-简体中文-red.svg)](README.zh-CN.md)

`dora` is a tiny, modular LLM agent kernel for Go. Its core is one loop and
two interfaces: `Model` and `Tool`.

No configuration file is required for the built-in providers: set the selected
provider's API key (for example `DEEPSEEK_API_KEY`), optionally select a model
with the per-capability `policy` setting (for example
`DORA_POLICY_TEXT_PROVIDER=deepseek`), and run immediately.

See [`docs/architecture.md`](docs/architecture.md) for module boundaries,
dependencies, interfaces, and runtime flows.

[DoraBar](https://github.com/lgxz/DoraBar) is a companion macOS tray/menu-bar
app for dora, written in Swift. It provides a lightweight menu-bar interface
for interacting with the dora CLI.

## Benchmark Results

dora is evaluated on [terminal-bench-2.1](https://github.com/harbor-framework/terminal-bench-2.1)(89 tasks). 
The table below tracks the mean pass rate across evaluation runs as dora evolves:

### deepseek-v4-flash (thinging disabled)
| Run | Date | Mean | Passed | Notes |
|-----|------|------|--------|-------|
| 1 | 2026-08-12 | 0.573 | 51/89 | Baseline |
| 2 | 2026-08-13 | 0.483 | 43/89 | Rate-limit affected |
| 3 | 2026-08-13 | 0.596 | 53/89 |  |
| 4 | 2026-08-14 | 0.607 | 54/89 | Background job support |
| 5 | 2026-08-14 | 0.618 | 55/89 | System prompt |
| 6 | 2026-08-14 | 0.685 | 61/89 | read/write/edit tools |

### deepseek-v4-pro (thinging: high)

| Run | Date | Mean | Notes |
|-----|------|------|--------|
| 1 | 2026-08-28 | 0.787 |  |

https://hub.harborframework.com/jobs/747f9a67-ed3a-498b-a70e-6786a70eb8a5

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
cat notes.md | dora Summarize the following content | mdcat
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
Model selection is driven by per-capability `policy`, not by a single model
selector. Each capability (`text` and `image`) has an optional `{provider,
profile}` pair; either field left empty falls back to automatic (`auto`)
selection. The environment override for a policy field is
`DORA_POLICY_<CAPABILITY>_<FIELD>` (for example `DORA_POLICY_TEXT_PROVIDER`,
`DORA_POLICY_TEXT_PROFILE`, `DORA_POLICY_IMAGE_PROVIDER`,
`DORA_POLICY_IMAGE_PROFILE`); environment values take precedence over the
config file.

For an interactive first-time setup, run:

```sh
dora --setup
```

The setup flow selects one of Dora's built-in providers, stores its API key
under the config-local `env` mapping, and sets `policy.text.provider`. Selecting
a concrete model profile is optional; pressing Enter leaves profile selection
automatic. Existing configuration fields and comments are preserved. The
configuration is written to `~/.dora/config.yaml` (or the path supplied with
`--config`) with owner-only file permissions on Unix. Process environment
variables continue to take precedence over stored values.

macOS / Linux, temporary (current terminal only):

```sh
export TRUST_API_KEY="sk-..."
export DORA_POLICY_TEXT_PROVIDER="trust"
export DORA_POLICY_TEXT_PROFILE="deepseek-v4-flash"
```

macOS / Linux, permanent: append the `export` lines above to `~/.zshrc` (zsh)
or `~/.bashrc` (bash), then reload it:

```sh
source ~/.zshrc
```

Windows PowerShell, temporary (current session only):

```powershell
$env:TRUST_API_KEY = "sk-..."
$env:DORA_POLICY_TEXT_PROVIDER = "trust"
$env:DORA_POLICY_TEXT_PROFILE = "deepseek-v4-flash"
```

Windows PowerShell, permanent (persists for the current user):

```powershell
[Environment]::SetEnvironmentVariable("TRUST_API_KEY", "sk-...", "User")
[Environment]::SetEnvironmentVariable("DORA_POLICY_TEXT_PROVIDER", "trust", "User")
[Environment]::SetEnvironmentVariable("DORA_POLICY_TEXT_PROFILE", "deepseek-v4-flash", "User")
```

Windows CMD, temporary (current session only):

```cmd
set TRUST_API_KEY=sk-...
set DORA_POLICY_TEXT_PROVIDER=trust
set DORA_POLICY_TEXT_PROFILE=deepseek-v4-flash
```

When a policy field is left to `auto`, Dora walks the catalog in order —
providers in the order they are listed, then that provider's profiles in the
order they are listed — and selects the first entry that both has a non-empty
API key (that is, is usable) and satisfies the capability constraint. Catalog
order is therefore priority. A provider with no API key is treated as
unavailable and is skipped during selection. Local endpoints that do not
require authentication (such as Ollama) still need a non-empty placeholder API
key to be selectable. With no configuration file and no keys, Dora retains the
built-in DeepSeek default.

To customize the defaults, create `~/.dora/config.yaml`. Dora uses this
`~/.dora/` layout on every operating system, including macOS. Set the
`DORA_HOME` environment variable to an absolute path to override the home
directory. You can also place the file anywhere and pass
`--config path/to/config.yaml`; an explicitly requested file must exist.

```yaml
env:
  DEEPSEEK_API_KEY: sk-...
```

This minimal file uses the embedded DeepSeek catalog and auto-selects the first
available text model. Replacing the key with `TRUST_API_KEY` selects Trust.
When both keys are configured, set `policy.text` explicitly:

```yaml
env:
  DEEPSEEK_API_KEY: sk-deepseek...
  TRUST_API_KEY: sk-trust...
policy:
  text:
    provider: trust
    profile: deepseek-v4-flash
```

The embedded provider catalog supplies built-in `deepseek`, `trust`, and `openrouter`
definitions with `base_url`, so catalog entries with those names may omit that
field. Models are always listed explicitly in `providers[].profiles`. Each
entry's `name` is a unique profile name used by `policy.*.profile`; `model` is
the identifier sent to the provider. `model` defaults to `name` when omitted,
and multiple profiles may use the same model with different parameters. Each
profile declares its capabilities with `capabilities`, for example
`capabilities: [text]` or `capabilities: [text, image_input]`. Set
provider-level or model-level `api: responses` to use the Responses API. Both
APIs always use SSE streaming.
Responses tool loops replay typed output items locally and do not depend on
server-side response storage.

Setting `OPENROUTER_API_KEY` makes the built-in `openrouter/auto` profile
available for automatic text and image selection. Catalog order remains
DeepSeek, Trust, then OpenRouter, so an available earlier provider still wins.
Select it explicitly with `-m openrouter/auto` or policy when desired. The
built-in `openrouter/ox-alpha` profile targets `stealth/ox-alpha` directly.

### Third-party OpenAI-compatible providers

To use any third-party provider that speaks the OpenAI Chat Completions
protocol (for example Ollama, LM Studio, vLLM, Groq, Together, or
a self-hosted endpoint), add it to `providers` with `base_url` and profiles. The Chat Completions endpoint is
`base_url + "/chat/completions"`, so `base_url` should be the provider's `/v1`
(or equivalent) root.

For a self-hosted Ollama endpoint that requires no authentication, leave
`OLLAMA_API_KEY` unset:

```yaml
providers:
  - name: ollama
    base_url: http://localhost:11434/v1
    profiles:
      - name: llama3.1
```

To replace OpenRouter's built-in `auto` profile with an explicit catalog, list
the provider in the config; user profile lists replace the built-in profiles:

```yaml
providers:
  - name: openrouter
    base_url: https://openrouter.ai/api/v1
    profiles:
      - name: balanced
        model: openrouter/auto
env:
  OPENROUTER_API_KEY: sk-or-v1-...
```

Even a local endpoint that does not require authentication needs a non-empty
API key (any placeholder value works) to be selectable. Provider names that
normalize to the same environment variable are rejected. Config files
containing keys are secrets and should be protected accordingly.

Control the normal per-response output budget, hard model output capacity, and
sampling with `max_tokens`, `max_output_tokens`, and `temperature`:

```yaml
providers:
  - name: openrouter
    base_url: https://openrouter.ai/api/v1
    profiles:
      - name: balanced
        model: openrouter/auto
        max_tokens: 32768
        max_output_tokens: 65536
        temperature: 0.7
policy:
  text:
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
`temperature` as best-effort. `max_output_tokens` is an optional positive model
capability with no default. It clamps both `max_tokens` and a request-specific
output limit. These keys are model catalog settings; there are no command-line
flags for them.

`context_window` belongs to a model profile and records that model's context
capacity in tokens. It defaults to 1048576, and configured values must be
positive. When a provider reports usage, Dora bases the next tool round on the
previous call's exact `total_tokens` plus an estimate for tool results produced
after that response. Without reported usage, Dora estimates the whole message
history and tool schemas. The provider-neutral estimate treats about four
ASCII bytes or one non-ASCII rune as a token and includes a small per-message
framing allowance; it does not estimate vision tokens. When predicted usage,
including an output reserve, reaches 80% of the context window, Dora asks the
active model to replace the model-visible history with a semantic summary of
at most 20% of the window, further capped by the profile's optional
`max_output_tokens`. That effective target is sent as a request-specific output
limit, overriding the ordinary `max_tokens` value so a provider's smaller
default cannot silently constrain compaction. The summary call has no tools or
continuation. A non-empty summary returned at the output limit is accepted. The
complete Turn remains unchanged for persistence, and a failed summary never
falls back to deleting or locally truncating history.

`thinking` controls the model's "thinking mode" reasoning effort. Set it to one
of `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. It has no default: when
omitted, no value is sent and the provider uses its own reasoning default.
Support varies by provider, and unsupported values are silently ignored rather
than causing an error:

- **openai**: on the Responses API all values are sent (`off` becomes `none`);
  on Chat Completions `minimal`–`max` are sent
  as `reasoning_effort`, but `off` is not supported (gpt-5's floor is
  `minimal`) and is ignored.
- **deepseek**: `low` through `max` are sent on both APIs, `minimal` is not
  supported and is ignored, and `off` is sent as `thinking.type: disabled` on
  Chat Completions or `reasoning.effort: none` on Responses.
- **trust**: treated best-effort like OpenAI on both APIs.
- **openrouter**: `minimal`–`max` are sent as `reasoning_effort` on Chat
  Completions, and `off` is sent as `reasoning_effort: none`.

Because a setting may simply be dropped, treat `thinking` as a hint rather
than a guarantee. For one invocation, `--thinking` overrides the selected
model's setting with one of `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`:

```sh
./dora --thinking high "Solve a hard problem"
```

`preserve_thinking` is a per-profile switch (default off) for reasoning models
on Chat Completions. When true, reasoning captured from earlier tool-calling
rounds is resent on those assistant history messages. Plain reasoning uses
`reasoning_content`; structured `reasoning_details` are kept in order without
semantic modification in the provider continuation and take precedence when
present. DeepSeek and OpenRouter's built-in profiles enable it. Providers that
ignore or reject resent reasoning (or expect it stripped, like Qwen/DashScope)
keep it off.

For one invocation, `-m`/`--model` overrides the configured conversation model
as `PROVIDER/PROFILE`, selecting a provider and an optional profile by name. A
bare `PROVIDER` (or trailing slash) selects that provider's default profile (the
first profile matching the requested capabilities). It does not change the
configuration file:

```sh
./dora -m trust/deepseek-v4-flash "Explain this repository"
./dora -m deepseek "Explain this repository"
```

`-m` only affects the text conversation model; the image and skill models are
unaffected. Unknown provider or profile names, or a provider with no usable API
key, result in `router: no model satisfies the constraints`.

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

Every Agent has an immutable system prompt. The binary ships a built-in default
(working habits such as verifying results against the literal request before
declaring a task complete); a non-empty `agent.system_prompt` fully replaces
it:

```yaml
agent:
  system_prompt: |
    You are a terminal assistant for the Acme deploy team.
    Always prefer the deploy scripts under /opt/acme.
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

When stdout is a terminal, Dora prints the final answer as plain text.
Redirected or piped stdout is identical, preserving stable output for scripts:

```sh
./dora "Write release notes"
./dora "Write release notes" > release-notes.md
```

Progress colors use `--color=auto` by default: they are enabled when stderr is
a terminal and `NO_COLOR` is unset. Use `--color=always` to preserve ANSI color
when stderr is redirected, or `--color=never` to disable it. An explicit color
mode overrides automatic terminal and environment detection; progress remains
visible on stderr in every mode. Before execution, progress identifies the
resolved conversation model as `Model PROVIDER/PROFILE · thinking=VALUE`; these
are the effective settings after policy, automatic availability filtering, and
CLI overrides. `thinking=default` means no thinking value is explicitly sent
to the provider. `--quiet` suppresses this line with the rest of the progress
output.

Reasoning models stream their chain-of-thought before the final answer. Dora
hides it by default because streaming it to the terminal slows runs on slow
terminals; pass `--reasoning` to show it live in a dim style in place of the
"Thinking..." placeholder. The final answer still goes to stdout on its own
line, and `--quiet` suppresses the reasoning display along with all other
progress.

When a provider reports token usage, Dora captures it for every completed model
round, including cached and reasoning-token details when available. The
terminal renderer does not print usage. Per-round usage and the final response
usage are saved in the active SQLite session and returned by the history tool;
without `--session`, that database is in memory and disappears when Dora exits.

Use `--workdir` to choose the base directory for relative paths used by tools:

```sh
./dora --workdir /path/to/project "Run the tests and inspect failures"
```

The directory must already exist. Dora resolves it to an absolute path without
changing the process working directory, so concurrent Agent runs can use
different bases safely. It applies to command tools and to relative paths used
by `read`, `write`, `edit`, `grep`, `glob`, and `view_image`. Absolute tool
paths are unchanged. Configuration and `--session` paths continue to be
resolved from the process working directory. `--workdir` selects a path
reference, not a filesystem sandbox or an additional permission boundary.

### Sessions

Pass a SQLite file to retain turns across CLI invocations:

```sh
./dora -s ./system-status.sqlite "Analyze this machine's system status"
./dora -s ./system-status.sqlite "Continue with the busiest processes"
```

Every invocation is a fresh, independent turn. Previous messages are never
loaded into the model context automatically. Dora always adds a `history` tool,
including before the first turn: the model can
`list` turns, see each turn's status, error, round count, and final-response usage,
and `get` chronological round pages using `turn_id`, `offset`, and `limit`; an
empty database returns an empty list. A round is one assistant tool-call message
plus all corresponding tool result messages and that model call's optional
usage. Successfully completed turns are appended atomically. A turn
stopped by the maximum-round limit is also saved with status `max_rounds`, its
error, and all completed tool rounds; it has no final result or final-response
usage. Ctrl+C cancellation is saved with status `canceled`; Dora uses a separate
short-lived commit context so canceling the run does not also cancel its session
write. Any other failed turn is saved with status `failed`. Both retain their
error and only the tool rounds that completed before termination; partial
streamed model output is not saved. Confirming the interactive continuation
prompt keeps using the same Turn and does not save an intermediate `max_rounds`
record. Provider continuation is kept only while that turn runs.

With `--session`, SQLite schema version 6 contains `turns` and `messages` tables and records the
turn status and error, system prompt, user input, final result, intermediate
tool rounds, reasoning captured on round assistant messages, and each model
call's usage JSON. Newly created files use `0600` permissions. The old named
JSON session format, `--fresh`, and automatic migration are not supported
(schema version 5 and earlier databases, plus development v6 files whose status
constraint predates `canceled`, are rejected; start a new file). When
`--session`/`-s` is omitted, Dora uses an in-memory SQLite database for the
process lifetime. This allows long-running modes to retain earlier turns while
keeping ordinary CLI invocations ephemeral. Session databases can contain
commands, tool output, and token usage, so treat persistent files as sensitive.

Use `--config`, `-m`/`--model`, `--thinking`, `--max-rounds`, or `--no-skills` to override the
corresponding configuration for one invocation.

### Agent Client Protocol

Run Dora as an ACP v1 agent over stdin/stdout:

```sh
dora --acp
```

An ACP client launches that command and exchanges newline-delimited JSON-RPC
on its standard streams. Configuration, model selection, `--thinking`,
`--max-rounds`, and `--no-skills` work as in the CLI. Protocol output owns
stdout; diagnostics remain on stderr.

Each `session/new` creates an isolated Agent, job manager, and in-memory SQLite
history store. The absolute `cwd` supplied by the client becomes that session's
tool working directory. Repeated `session/prompt` calls create independent Dora
Turns whose earlier results remain available through the history tool. Sessions
can run concurrently, while a single session rejects overlapping prompts.

The initial ACP surface supports initialization, session creation, text and
resource-link prompts, streamed answer and thought chunks, tool-call lifecycle
updates, cancellation, and session close. Reaching Dora's maximum round count
returns the ACP `max_turn_requests` stop reason. Closing a session cancels its
active prompt and background jobs and discards its in-memory history.

Clients that advertise terminal authentication receive a `Configure Dora`
method which launches `dora --setup`. Authentication remains local: the setup
flow configures the selected model provider and the ACP connection never
receives the API key. Dora can complete `initialize` without a configured
model; `session/new` returns `Authentication required` until setup is complete
and the client reconnects.

This first version does not support protocol-driven authentication/logout,
persistent session listing/resume/load, prompt images/audio/embedded resources,
modes, config options, client filesystem/terminal delegation, or MCP servers
supplied by the client. Non-empty `mcpServers` and `additionalDirectories` are rejected.
`--acp` cannot be combined with a prompt, `--session`, `--workdir`, or
`--events`; ACP supplies its own prompts, sessions, and working directories.

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
  powershell:
    enabled: true
  task:
    enabled: false
```

Automatic tools whose executable is absent are skipped. A tool explicitly
enabled with `enabled: true` must exist on `PATH`, otherwise Dora reports an
error. Discovery currently checks executable presence only; it does not launch
the shell to probe its runtime environment.

The Bash tool runs `bash -lc` in the directory selected by `--workdir`, or in
Dora's process working directory when the option is omitted. The model can use
`cd` inside a command when it needs another directory. The tool returns exit
code, stdout, and stderr to the model as JSON. This tool grants the model the
same filesystem and process permissions as the `dora` process, so disable it
unless you trust the environment in which Dora runs.

The independent `powershell` tool prefers PowerShell Core (`pwsh`) and falls
back to Windows PowerShell (`powershell.exe`). If both tools are explicitly
enabled, they are exposed separately so their command syntaxes remain distinct.

PowerShell uses the same working-directory rule and can use `Set-Location`
inside a command when needed.

Both command tools use a single `wait_seconds` knob. It is the maximum number
of seconds to wait for the command to finish before moving it to the
background; once in the background it continues running (it is not terminated)
and Dora returns a `job_id` that the job tool can use to inspect it. Background
processes are also not terminated when Dora itself exits — they keep running
after the session ends, so stop them explicitly with the job tool's kill action
when needed. On Unix, kill terminates the command's process group; on Windows,
it terminates the process tree. This covers ordinary descendants such as
`timeout 240 python3 ...`. Stdout and stderr are each capped at 32 KiB per
result (head and tail kept, with a marker naming the original size) in command
results, background-adoption results, and job polls; redirect larger output to
a file to inspect it. By default (omitted) Dora waits 10 seconds, and a
value of `0` moves the command to the background immediately:

```json
{
  "command": "go build ./...",
  "wait_seconds": 300
}
```

Command job IDs are prefixed by the source tool and counted independently, for
example `bash_0` and `powershell_0`. The public job results do not include a
separate job-kind field; the ID identifies the source.

## Independent tasks

The `task` tool is enabled by default. It accepts one self-contained
`instruction`, runs it through the same Agent and model in a fresh `Turn`, and
returns that turn's final text to the parent conversation. The fresh turn does
not inherit the parent's messages or provider continuation. It uses the same
immutable Agent system prompt and can use every other
tool available to the parent, but `task` itself is removed from both the tool
definitions and executable tool set to prevent recursive delegation.

Multiple task calls in one model response run concurrently, following Dora's
normal tool execution semantics. There is no separate task concurrency limit.
Child progress is not sent to the terminal Observer; only the parent message
and the completed task summary and duration are rendered.

Set `background` to `true` when the parent should continue immediately instead
of waiting for the independent turn:

```json
{
  "instruction": "Run the full test suite and summarize failures",
  "background": true
}
```

The result immediately contains a `task_N` job ID. Use the same `job` tool to
list, poll, or kill it; `job.poll.wait_seconds` controls how long that poll
waits, while Task itself has no duration estimate. A completed Task result is
retained for repeated polls. Unlike background shell processes, background
Tasks run inside Dora and stop—with their uncollected result lost—when the Dora
process exits. Their nested progress remains hidden, including while they run
in the background. Killing a Task cancels its context cooperatively. The job
reports `cancelling` until the nested run actually returns and only then reports
`killed`; a component that ignores context cancellation can therefore delay
termination.

Task context isolation is not a security sandbox. Parent and child runs share
the process, current working directory, filesystem, permissions, model client,
and concrete tool instances. Disable the tool when delegation is not wanted:

```yaml
tools:
  task:
    enabled: false
```

## Image understanding

Dora treats vision support as a model-profile capability. A profile that can
understand images declares `capabilities: [text, image_input]`; a text-only
model declares `capabilities: [text]`. There is no CLI override or direct
image-attachment flag.

When the model wants to view an image, it calls the `view_image` tool, which is
always registered. The tool accepts a local `path` or a remote `url` and routes
the image through a transient visual model selected with the `image_input`
capability constraint, returning a text description of the image. The image
itself never enters the main model's context — only the returned description
does.

Dora limits each local image file to 4 MiB and rejects files that are not
images. When a `view_image` call points at a missing, non-image, or oversized
file, the tool reports the error to the model so it can correct the path. If no
catalog entry advertises `image_input`, the `view_image` call reports that no
visual model is available.
