# dora

`dora` is a tiny, modular LLM agent kernel for Go. Its core is one loop and
two interfaces: `Model` and `Tool`.

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

Build the command:

```sh
go build -o dora ./cmd/dora
```

Create the configuration file at
`${XDG_CONFIG_HOME:-$HOME/.config}/dora/config.yaml`. Dora uses this XDG layout
on every operating system, including macOS.

You can also place it anywhere and pass `--config path/to/config.yaml`.

```yaml
model:
  provider: openai-responses
  name: gpt-5
  base_url: https://api.openai.com/v1
  api_key_env: OPENAI_API_KEY
```

Use `openai-responses` for OpenAI's Responses API with SSE streaming. The
existing `openai-compatible` provider continues to use Chat Completions for
compatible third-party and local endpoints. Responses tool loops replay typed
output items locally and do not depend on server-side response storage.

Literal `api_key` is also supported, but an environment variable keeps secrets
out of the configuration file. The two options are mutually exclusive. API
keys are optional for local endpoints that do not require authentication.

Dora allows up to 64 model turns per task by default. Keep the safeguard but
adjust it for unusually long tool workflows when needed:

```yaml
agent:
  max_model_calls: 96
```

Run a one-shot prompt or combine an instruction with piped input:

```sh
export OPENAI_API_KEY="..."
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
session as a versioned JSON snapshot with `0600` permissions. Session v2 binds
the configured provider, model, and base URL: Chat Completions resumes from
messages, while Responses additionally persists its opaque typed-item
continuation. Use `--fresh` before changing a session's backend. Version 1
session files are not supported. The default directory is
`${XDG_STATE_HOME:-$HOME/.local/state}/dora/sessions` on every operating
system. Omit `--session`/`-s` to keep the existing stateless behavior. Session
files can contain commands and tool output, so treat them as sensitive. Do not
run two Dora processes against the same session name concurrently.

Use `--config`, `--model`, or `--base-url` to override the corresponding
configuration for one invocation.

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

Dora advertises only skill names and descriptions in the `skill` tool schema.
The absolute skill directory and complete `SKILL.md` are returned when the
model calls that tool, allowing instructions to reference files such as
`scripts/check.sh`. The skill tool never executes those files; execution still
requires an enabled tool such as Bash. Names must contain lowercase letters,
numbers, and hyphens, and must match their directory name. Duplicate names are
rejected. A missing or empty default directory simply leaves the tool disabled;
malformed discovered skills and missing explicitly configured directories are
errors.

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

### Bash tool

The Bash tool is disabled by default. Enable it explicitly when the model
should be allowed to execute commands:

```yaml
tools:
  bash:
    enabled: true
    working_dir: /path/to/project
    timeout_seconds: 30
```

The tool runs `bash -lc` in the configured directory. It returns exit code,
stdout, stderr, timeout, and truncation information to the model as JSON.
Output is limited to 1 MiB per stream. Enabling this tool grants the model the
same filesystem and process permissions as the `dora` process, so only enable
it in an environment you trust.
