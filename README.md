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

Use `--config`, `--model`, or `--base-url` to override the corresponding
configuration for one invocation.
