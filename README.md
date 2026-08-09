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
