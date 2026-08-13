# dora

`dora` is a tiny, modular LLM agent kernel for Go. Its core is one loop and
two interfaces: `Model` and `Tool`.

By default the CLI exposes only the Bash tool on macOS/Linux or the PowerShell
tool on Windows; when skill directories are present, the skill tool is
activated automatically, and there are no other tools. The CLI never injects a
system prompt, sending only the user's prompt as a user message. No
configuration file is required: setting a single provider API key environment
variable (OpenAI, DeepSeek, or Trustoken) lets dora auto-select that provider
and run immediately.

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

[DoraBar](https://github.com/lgxz/DoraBar) is a companion macOS tray/menu-bar
app for dora, written in Swift. It provides a lightweight menu-bar interface
for interacting with the dora CLI.

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

Dora reads each provider's API key from a dedicated environment variable. The
following table lists the supported providers, their environment variables,
and their default models:

| Provider | Environment variable | Default model |
| --- | --- | --- |
| openai | `OPENAI_API_KEY` | `gpt-5` |
| deepseek | `DEEPSEEK_API_KEY` | `deepseek-v4-flash` |
| trust | `TRUST_API_KEY` | `auto` |

Set the environment variable for the provider you want to use. The commands
differ by operating system.

macOS / Linux, temporary (current terminal only):

```sh
export OPENAI_API_KEY="sk-..."
```

macOS / Linux, permanent: append the `export` line above to `~/.zshrc` (zsh)
or `~/.bashrc` (bash), then reload it:

```sh
source ~/.zshrc
```

Windows PowerShell, temporary (current session only):

```powershell
$env:OPENAI_API_KEY = "sk-..."
```

Windows PowerShell, permanent (persists for the current user):

```powershell
[Environment]::SetEnvironmentVariable("OPENAI_API_KEY", "sk-...", "User")
```

Windows CMD, temporary (current session only):

```cmd
set OPENAI_API_KEY=sk-...
```

When you set keys for more than one provider, specify `model.provider`
explicitly in `~/.dora/config.yaml`; otherwise Dora reports an ambiguity
error. Setting exactly one key lets Dora select that provider automatically.

With exactly one supported provider API key set, Dora runs without a
configuration file and selects that provider automatically. For example,
`DEEPSEEK_API_KEY` selects `deepseek`, `OPENAI_API_KEY` selects `openai`, and
`TRUST_API_KEY` selects `trust`. If multiple provider keys are set, configure
`model.provider` explicitly. If none are set, Dora retains `deepseek` as the
fallback and reports that `DEEPSEEK_API_KEY` is missing.

To customize the defaults, create `~/.dora/config.yaml`. Dora uses this
`~/.dora/` layout on every operating system, including macOS. Set the
`DORA_HOME` environment variable to an absolute path to override the home
directory. You can also place the file anywhere and pass
`--config path/to/config.yaml`; an explicitly requested file must exist.

```yaml
model:
  provider: deepseek
```

An explicit provider always takes precedence over environment-based selection.
The same automatic selection applies when a configuration file exists but
omits `model.provider`.

The `deepseek` preset defaults to the `chat_completions` API,
`deepseek-v4-flash`, `https://api.deepseek.com`, and `DEEPSEEK_API_KEY`. The
`openai` preset defaults to `chat_completions`, `gpt-5`,
`https://api.openai.com/v1`, and `OPENAI_API_KEY`. The `trust` preset defaults
to `chat_completions`, `auto`, `https://api.trustoken.cn/v1`, and
`TRUST_API_KEY`. Override any preset field when needed, and set
`api: responses` to use the Responses API. Both APIs always use SSE streaming.
Responses tool loops replay typed output items locally and do not depend on
server-side response storage.

### Third-party OpenAI-compatible providers

To use any third-party provider that speaks the OpenAI Chat Completions
protocol (for example Ollama, LM Studio, vLLM, Groq, Together, OpenRouter, or
a self-hosted endpoint), keep `model.provider: openai` and override `base_url`,
`name`, and `api_key_env` (or `api_key`). The Chat Completions endpoint is
`base_url + "/chat/completions"`, so `base_url` should be the provider's `/v1`
(or equivalent) root.

For a self-hosted Ollama endpoint that requires no authentication, set
`api_key_env: ""` to disable the API key:

```yaml
model:
  provider: openai
  name: llama3.1
  base_url: http://localhost:11434/v1
  api_key_env: ""
```

For a hosted OpenAI-compatible service that requires a key, such as OpenRouter
or Groq, point `api_key_env` at a custom environment variable:

```yaml
model:
  provider: openai
  name: openrouter/auto
  base_url: https://openrouter.ai/api/v1
  api_key_env: OPENROUTER_API_KEY
```

For a one-off invocation, override the model and base URL on the command line:

```sh
./dora --model llama3.1 --base-url http://localhost:11434/v1 "prompt"
```

Literal `api_key` is also supported, but an environment variable keeps secrets
out of the configuration file. A non-empty literal key takes precedence over
`api_key_env`. Set `api_key_env: ""` explicitly for a local endpoint that does
not require authentication.

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

By default Dora sends the most recent 32 rounds to the model each iteration and
compresses older history so a long tool loop does not grow the context without
bound. Override the number of retained rounds for one invocation with
`--max-history-rounds` (use `0` to disable compaction and send the full
history):

```sh
./dora --max-history-rounds 64 "Complete a long task"
```

When the limit is reached with both stdin and stderr attached to a terminal,
Dora asks whether to continue for another segment. Confirming resumes from the
completed tool output without replaying work. Declining stops normally and
saves the partial state of a named session. With piped or redirected I/O, Dora
does not prompt and returns `dora: maximum rounds exceeded` instead.

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
`~/.dora/sessions` on every operating system. Omit `--session`/`-s` to keep
the existing stateless behavior. Session files can contain commands and tool
output, so treat them as sensitive. Do not run two Dora processes against the
same session name concurrently.

Use `--config`, `--model`, `--base-url`, `--max-rounds`, `--max-history-rounds`,
`--skills-dir`, or `--no-skills` to override the corresponding configuration
for one invocation.

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

By default, Dora discovers the `skills` directory at `~/.dora/skills` (or
`DORA_HOME/skills`), independent of the active `config.yaml` path. No
configuration is needed.

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

Dora can attach images to messages so a multimodal (vision) model can "see"
them. Image understanding depends on the configured model: the default
DeepSeek model may not support vision, so enable it explicitly with `--vision`
or `model.vision: true` in the config, and select a vision-capable model (for
example an OpenAI `gpt-4o`-class model).

Attach a local image to the current prompt with the repeatable `--image` flag
(requires vision to be enabled):

```sh
./dora --vision --model gpt-4o --image photo.png "Describe this photo"
```

The model can also surface images itself: when a command tool's stdout contains
a `@@path@@` tag, Dora parses it and attaches the image at that path to the
tool message. The command tool description documents this convention so the
model knows it can emit such a tag.

Images consume model context: each attached image is encoded into vision tokens
that count toward the model's context window, so large images or many images
can exhaust a small context window. Dora limits each image file to 4 MiB and
rejects files that are not images. When a `@@path@@` tag points at a missing,
non-image, or oversized file, Dora does not attach it and instead reports the
error to the model so it can correct the path.
