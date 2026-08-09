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

The first version deliberately has no streaming events, built-in memory,
policies, middleware, provider SDKs, or parallel tool execution.

## CLI

Build the command:

```sh
go build -o dora ./cmd/dora
```

Create the configuration file at the platform config directory:

- macOS: `~/Library/Application Support/dora/config.yaml`
- Linux: `${XDG_CONFIG_HOME:-~/.config}/dora/config.yaml`

You can also place it anywhere and pass `--config path/to/config.yaml`.

```yaml
model:
  provider: openai-compatible
  name: gpt-5
  base_url: https://api.openai.com/v1
  api_key_env: OPENAI_API_KEY
```

Literal `api_key` is also supported, but an environment variable keeps secrets
out of the configuration file. The two options are mutually exclusive. API
keys are optional for local endpoints that do not require authentication.

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

Session names may contain letters, numbers, `.`, `_`, and `-`. Dora stores each
session as a versioned JSON snapshot with `0600` permissions. On macOS the
default directory is `~/Library/Application Support/dora/sessions`; on Linux it
is `${XDG_STATE_HOME:-~/.local/state}/dora/sessions`. Omit `--session`/`-s` to
keep the existing stateless behavior. Session files can contain commands and
tool output, so treat them as sensitive. Do not run two Dora processes against
the same session name concurrently.

Use `--config`, `--model`, or `--base-url` to override the corresponding
configuration for one invocation.

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
