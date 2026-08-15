# Dora Architecture

This document describes the module boundaries, dependencies, primary interfaces, and runtime flow of the current Dora implementation. It records the current state, not future plans; when code behavior changes, this document should be updated accordingly.

## Design Goals

Dora is a small, composable LLM agent. The core design principles are:

- The core loop depends only on a small set of interfaces, not on any concrete model, tool, CLI, or storage implementation.
- Each package has a single primary responsibility; cross-package access happens through explicit interfaces or data structures.
- The CLI is the composition root, responsible for selecting and assembling implementations; the core packages do not read configuration or environment variables.
- Session state is explicitly passed in and retrieved by the caller; `Agent` itself holds no mutable state across tasks.
- All external operations accept `context.Context`, supporting cancellation and timeouts.

## Overall Structure

```mermaid
flowchart TD
    Main["cmd/dora<br/>process entry"] --> CLI["internal/cli<br/>parsing, assembly, orchestration"]

    CLI --> Config["internal/config<br/>YAML config"]
    CLI --> Paths["internal/paths<br/>~/.dora paths"]
    CLI --> Session["internal/session<br/>session snapshots"]
    CLI --> Update["internal/update<br/>standalone self-update"]
    CLI --> Progress["internal/progress<br/>terminal progress"]
    CLI --> Core["dora<br/>Agent core"]
    CLI --> OpenAI["model/openai<br/>Chat Completions"]
    CLI --> Responses["model/openairesponses<br/>Responses API"]
    CLI --> Skill["skill<br/>Skill tool"]
    CLI --> Bash["tool/bash<br/>Bash tool"]
    CLI --> PowerShell["tool/powershell<br/>PowerShell tool"]

    Bash --> CommandExec["tool/internal/commandexec<br/>command execution kernel"]
    PowerShell --> CommandExec

    OpenAI -->|"StreamingModel"| Core
    Responses -->|"StreamingModel"| Core
    Skill -->|"Tool"| Core
    Bash -->|"Tool"| Core
    PowerShell -->|"Tool"| Core
    CommandExec -->|"Tool"| Core
    Progress -->|"Observer"| Core
    Session -->|"Message / State"| Core
```

Key constraints on dependency direction:

- The root package `dora` does not import any concrete implementation package within the project.
- Model adapters and tool packages depend on the interfaces and data structures in `dora`.
- `internal/*` serves only the Dora application and cannot be imported directly from outside the module.
- `internal/cli` knows all concrete implementations and is currently the only composition root.

## Directories and Modules

| Module | Responsibility | Primary interface or entry point |
| --- | --- | --- |
| `dora` | Domain abstractions for the agent loop, messages, models, tools, and observers | `New`, `NewWithConfig`, `Agent.Run*`, `Model`, `Tool`, `Observer` |
| `cmd/dora` | Process startup, signal handling, terminal capability detection, final error output | `main` |
| `internal/cli` | Argument parsing, dependency assembly, session restore and save, input/output orchestration | `Run(context.Context, []string, IO)` |
| `internal/config` | Strict reading, parsing, and validation of YAML configuration | `Load(string)` |
| `internal/paths` | Resolves the unified `~/.dora` default paths on all platforms | `ConfigFile`, `SessionsDir`, `SkillsDir` |
| `internal/session` | Persists named sessions, validates version and concurrent revision | `New`, `Store.Load`, `Store.Revision`, `Store.Save` |
| `internal/update` | Queries stable Releases, validates archives, and replaces the standalone binary with rollback | `New`, `Service.Update` |
| `internal/progress` | Renders semantic run events as terminal output | `New`, `Renderer.Observe` |
| `model/openai` | OpenAI-compatible Chat Completions SSE protocol adapter | `New`, `Client.GenerateStream` |
| `model/openairesponses` | Responses API, SSE stream, and provider continuation adapter | `New`, `Client.GenerateStream` |
| `skill` | Discovers and validates local `SKILL.md`, returns full instructions to the model on demand | `New`, returns `dora.Tool` |
| `tool/bash` | Executes Bash within the current directory and under timeout and output-limit constraints | `New`, `Tool.Spec`, `Tool.Execute` |
| `tool/powershell` | Executes PowerShell using `pwsh` or `powershell.exe` | `New`, `Tool.Spec`, `Tool.Execute` |
| `tool/internal/commandexec` | Implements input validation, timeout, cancellation, output limits, and structured results for command tools | `New`, `Tool.Spec`, `Tool.Execute` |

## Core Interfaces

### Model

```go
type Model interface {
    Generate(context.Context, Request) (Response, error)
}

type StreamingModel interface {
    Model
    GenerateStream(context.Context, Request, func(ModelEvent)) (Response, error)
}
```

`Model` is the minimal interface that every model implementation must satisfy. `StreamingModel` is an optional capability; when the Agent detects it, it uses the streaming method, otherwise it falls back to `Generate`.

`Request` contains the complete provider-neutral messages, available tool definitions, and an opaque `Continuation`. `Response` can contain both text and multiple tool calls.

### Tool

```go
type Tool interface {
    Spec() ToolSpec
    Execute(context.Context, json.RawMessage) (string, error)
}
```

`Spec` exposes the name, description, and JSON Schema to the model. `Execute` receives only the model-generated JSON arguments and returns a text result. The core Agent does not know the concrete type of a tool.

Within the same Agent, tool names must be non-empty and unique. Tool definitions are copied when the Agent is constructed, preventing the caller from later modifying their JSON Schemas.

### Observer

```go
type Observer interface {
    Observe(Update)
}
```

The Observer synchronously receives semantic events such as `thinking`, content deltas, message additions, tool starts, and tool failures. It only consumes run data, does not participate in Agent decisions, and cannot modify the session history.

Callbacks run on the Agent's current goroutine, so implementations should return quickly. `internal/progress.Renderer` is the Observer used by the CLI.

### State and Result

```go
type State struct {
    Messages     []Message
    Continuation string
}

type Result struct {
    Content      string
    Messages     []Message
    Continuation string
}
```

`State` is the complete input state for a run, and `Result` returns the complete state that can be used directly for the next run. `Continuation` is owned by the provider; both the Agent and session storage treat it as an opaque string.

## Agent Loop

```mermaid
sequenceDiagram
    participant C as CLI / caller
    participant A as Agent
    participant M as Model
    participant T as Tool
    participant O as Observer

    C->>A: RunState(state)
    loop up to MaxRounds rounds
        A->>O: thinking
        A->>M: Request(messages, tools, continuation)
        M-->>O: content delta (optional)
        M-->>A: Response(content, tool calls, continuation)
        A->>O: assistant message
        alt no tool calls
            A-->>C: Result
        else tool calls present
            loop execute each in returned order
                A->>O: tool started
                A->>T: Execute(input)
                T-->>A: output
                A->>O: tool message
            end
        end
    end
```

Current execution semantics:

- The Agent is immutable; run history is kept in local variables.
- Input messages, tool calls, and output state are all defensively copied.
- When the model returns multiple tool calls, they are executed concurrently, but their results and Observer events are emitted in the returned order. Tools must be safe for concurrent use; the built-in command and skill tools are.
- Content from both APIs can be displayed as it is received, but tools must wait until the entire model response has finished before execution begins.
- A tool execution error, an unknown tool, or invalid JSON tool arguments does not terminate the task: the Agent feeds the failure back to the model as a `tool` message so the model can correct itself, and continues the loop. A tool itself may choose to encode a command failure as a normal result. For example, Bash returns a non-zero exit code to the model rather than terminating the Agent directly.
- If the model keeps calling tools, after reaching `MaxRounds` it returns both `ErrMaxRounds` and a resumable `Result` containing the completed tool output; the default limit is 256. When both stdin and stderr are connected to a terminal, the CLI asks whether to continue the next segment from that state; when the user declines, it saves the partial state of the named session and stops normally. Non-interactive runs keep reporting the error directly, avoiding waiting for input in pipelines or automated tasks.
- The current CLI does not inject a system prompt. `Message` and both API adapters support the `system` role, so library callers can pass one in themselves.

## CLI Run Flow

`internal/cli.Run` is responsible for the complete lifecycle of one command:

1. Parse arguments; `--version` and `-update` complete and exit before reading configuration or the prompt.
2. For a normal Agent run, compose the user prompt from command arguments and standard input.
3. Resolve the default or explicit configuration path; when the default file does not exist, use the built-in DeepSeek configuration, and when it does exist, strictly load the YAML. An explicitly specified configuration file that does not exist still reports an error.
4. Apply one-shot overrides such as `--model`, `--base-url`, and `--max-rounds`.
5. If a session is specified, read the snapshot and validate the provider, API, model, and base URL.
6. Create the concrete model adapter based on `model.api`; the provider is responsible for supplying provider defaults.
7. Discover skills and create the available tools according to configuration.
8. Construct a stateless `dora.Agent`.
9. Compose `State` from the historical messages and the current user message, then run the Agent.
10. On success, atomically save the session; write the final text to stdout verbatim.

The CLI's standard output carries only the final result; run progress and errors are written to standard error. TTY output is consistent with piped and redirected output, so results can still be safely used in scripts.

`cli.IO` injects the standard streams, build version, stdin/stdout/stderr terminal capabilities, stdout width and color, HTTP client, test updater, and session directory as dependencies, allowing the CLI to be tested without depending on process-global state.

`internal/update` only updates binaries marked by the standalone installer writing a marker in the same directory. It fetches the latest stable Release from GitHub, selects the archive for the running platform, verifies the SHA-256 using `checksums.txt`, and stages and runs the new version's `--version` in the same directory. After successful verification, it switches the binary via a same-directory rename; on installation failure it attempts a rollback and uses an exclusive marker to reject concurrent updates. Development builds, manual copies, and package-manager installations are not modified.

## Model Adapters

### Chat Completions

`model/openai` converts Dora messages and tool structures into `/chat/completions` requests. It always requests an SSE stream, aggregates text and chunked tool arguments into a complete response, and implements `StreamingModel`; cross-task resumption relies on the complete message history, and `Continuation` is empty.

### Responses API

`model/openairesponses` calls `/responses` and parses SSE. It implements `StreamingModel`, passes text deltas to the Agent, and encodes the typed items from the Responses protocol into an opaque continuation.

This continuation is used to preserve data across different CLI processes, such as reasoning, function calls, and function call output, which cannot be fully expressed by generic messages alone. It belongs only to the provider and backend that created it and should not be parsed or modified by other modules.

Both adapters are themselves responsible for:

- the endpoint and authentication headers;
- conversion between Dora messages and protocol messages;
- protocol wrapping of Tool JSON Schemas;
- interpretation of HTTP status and malformed response formats;
- response size or stream event size limits.

Sampling and output-cap settings are per-model configuration threaded from
`config.Model.MaxTokens` and `config.Model.Temperature` into each adapter's
`Config` and emitted in the request body with `omitempty` pointers. `temperature`
has no default and is only sent when explicitly configured, leaving the provider
default sampling otherwise. `max_tokens` defaults to 32768 in the config layer
and is therefore always sent. Because the two wire protocols use different key
names, the adapters map the single config concept to the correct key:
`max_tokens` for Chat Completions and `max_output_tokens` for the Responses API;
`temperature` is common to both. An explicit `max_tokens: 0` is relayed as-is,
meaning "no explicit cap."

Thinking-mode reasoning effort follows the same pattern. The CLI maps a single
`config.Model.Thinking` value (`off | minimal | low | medium | high`, nil by
default) to each protocol's control: Chat Completions emits `reasoning_effort`
for `minimal`–`high` and DeepSeek's `thinking.type: disabled` for `off`;
Responses emits a nested `reasoning` object with `effort: none` for `off` and
the effort value otherwise. The mapping is provider-aware and drops values a
provider does not support (for example DeepSeek ignores `minimal`; OpenAI Chat
Completions ignores `off`), so unsupported settings are silently absent from the
wire rather than erroring. The per-provider policy lives in `internal/cli`, and
the adapters only render the pre-computed fields.

The adapters do not validate the JSON of model-emitted tool-call arguments; they
pass the raw arguments through to the Agent, which is the single authority on
tool-call validity. Invalid arguments are fed back to the model as a recoverable
`tool` message rather than aborting the run.

## Session

`internal/session` saves one named task in a single versioned JSON file:

```text
~/.dora/sessions/<name>.json
```

The snapshot contains:

- the format version and a monotonically increasing revision;
- the backend identity composed of provider, API, model, and base URL;
- the provider-neutral complete messages;
- the provider-specific continuation.

Saving uses a same-directory temporary file, `fsync`, and an atomic rename, with directory permissions `0700` and file permissions `0600`. `Save` uses the expected revision to detect concurrent overwrites but does not provide cross-process locking; when two processes operate on the same named session simultaneously, one of them will eventually receive a conflict error.

Normal resumption requires the backend to match exactly. `--fresh` ignores the old content and replaces the file with the new format and backend after the task succeeds. Session v1 does not support resumption, but it can be explicitly replaced via `--fresh`.

## Skills

A skill is a tool, not text spliced directly into the prompt at startup. This way the model initially sees only the skill name and description, and only calls the `skill` tool to load the full content after deciding it is relevant.

By default, `~/.dora/skills` (or `DORA_HOME/skills`) is discovered first, followed by `~/.agents/skills/`. Each default directory is included only when it exists as a directory; an absent default is silently skipped. When `skills.directories` is configured, the defaults are replaced by exactly those directories (each must be an absolute path or start with `~/`). All resulting paths are converted to absolute form and deduplicated; `--no-skills` takes precedence over all sources and disables skills entirely. Each direct subdirectory must contain a valid `SKILL.md`:

- YAML front matter allows only `name` and `description`;
- the skill name must match the directory name and be globally unique;
- the file must be a regular file and satisfy the count, size, and description-length limits;
- when no default directory exists, or no directory contains a subdirectory with a `SKILL.md`, the entire tool stays disabled;
- when an explicitly configured directory does not exist or is not an absolute/`~/` path, or a skill is malformed, startup fails.

The tool execution result contains the skill's absolute directory and the complete `SKILL.md`, so instructions can reference scripts in the same directory. The skill tool itself does not execute files; execution still requires another explicitly enabled tool, such as Bash.

## Bash Tool

Bash is enabled automatically on Linux and macOS and disabled automatically on other platforms. When it is auto-enabled but the `bash` executable cannot be found, the CLI skips the tool; `enabled: true` can explicitly enable it on any platform, in which case a missing executable causes startup to fail. Discovery currently only checks `PATH` and does not launch a shell to probe the runtime environment. When the tool is available, each invocation executes via `bash -lc` in Dora's current directory; when the model needs to change directories, it uses `cd` within the command. The tool has the following boundaries:

- a default timeout of 120 seconds;
- each tool call can override the configured default with `timeout_seconds`, ranging from 1 to 3600 seconds;
- stdout and stderr are each subject to output limits;
- context cancellation terminates the child process;
- the result returns the exit code, stdout, stderr, timeout, and truncation status as JSON;
- a non-zero command exit is a tool result the model can handle; infrastructure errors such as startup failures are returned as Go errors.

Bash is not a security sandbox. Enabling it is equivalent to allowing the model to execute commands with the system privileges of the Dora process.

## PowerShell Tool

PowerShell is a separate `powershell` tool from Bash, and its input Schema likewise accepts only `command`, preventing the model from mixing the syntax of the two shells in a single call. It is enabled automatically on Windows and disabled automatically on other platforms, and looks for `pwsh` and then `powershell.exe` in order. In automatic mode, when neither exists the CLI skips the tool; when explicitly enabled, it reports an error. Bash and PowerShell can be exposed simultaneously only when the user explicitly overrides the platform policy.

PowerShell executes commands in Dora's current directory using `-NoLogo -NoProfile -NonInteractive -Command`, and the model uses `Set-Location` within the command to change directories. It shares the same 120-second configured default timeout, the 1-to-3600-second per-call override range, output limits, and structured results as Bash, and is likewise not a security sandbox.

Bash and PowerShell remain separate public tools with separate shell-launch policies, and both delegate to `tool/internal/commandexec` for input validation, timeout and cancellation, process execution, output truncation, and result encoding. This internal package cannot be imported from outside the module.

## Configuration and Paths

`internal/config` provides built-in provider presets and uses strict YAML decoding to override defaults: unknown fields, multiple documents, invalid providers, invalid API types, and negative limits all report errors. When no provider is specified, the configuration layer directly reuses the API key environment variable mapping from the preset: if only one environment variable is non-empty, the corresponding provider is selected; if several are non-empty, an explicit selection is required; if all are empty, it falls back to DeepSeek. The command tools' `enabled` is a three-state configuration: when absent, the CLI applies the platform policy; an explicit `true` or `false` fully overrides it. The `openai`, `deepseek`, and `trust` providers supply their own endpoint, model, and API key environment variable defaults; explicit fields override the defaults. The API key can be configured directly or read at runtime from a specified environment variable, with a non-empty direct configuration taking precedence. When the default configuration path does not exist, the CLI uses the built-in configuration directly; an explicit `--config` does not silently fall back.

`internal/paths` uses a unified `~/.dora` layout on all operating systems:

| Content | Default path |
| --- | --- |
| Optional configuration | `~/.dora/config.yaml` |
| Skills | `~/.dora/skills` |
| Sessions | `~/.dora/sessions` |

The `DORA_HOME` environment variable can override the home directory and must be an absolute path. An explicit `--config` does not depend on the default configuration path and can fully override it.

## Extension Approaches

### Adding a model provider

1. An OpenAI-protocol-compatible provider only needs to register the provider and its default endpoint, model, and API key environment variable in a configuration preset.
2. A new protocol requires implementing `dora.StreamingModel` in a separate `model/<protocol>` package.
3. Encapsulate protocol-specific state in `Continuation`; do not leak it into the Agent core.
4. Create the implementation in the composition logic of `internal/cli`.
5. Add tests for defaults, protocol conversion, error responses, and CLI assembly.

### Adding a tool

1. Implement `dora.Tool` in a separate package.
2. Define a strict JSON Schema for the input and strictly parse the `Execute` arguments.
3. Add an explicit enable option and resource limits to the configuration.
4. Assemble the tool only in `internal/cli`; do not modify the Agent loop.

### Adding a new run interface

A new UI can call the root-package Agent directly and implement its own `Observer`. It does not need to depend on the terminal renderer and can decide for itself how to store `State`.

## Current Boundaries

- The Agent loop is single-goroutine, but the tool calls within one model response are executed concurrently (results are collected by index and emitted in order).
- HTTP request asynchrony is determined by the caller's goroutine; the core has no task scheduler.
- Both Chat Completions and Responses support streaming text display, but completed tool calls are not yet executed early while the stream is still running.
- Sessions are complete JSON snapshots rather than append-only logs, with a maximum of 64 MiB.
- Sessions have no cross-process locking; they only prevent silent overwrites via revision.
- The CLI is the composition root; as providers and tools grow, a factory/registry could be extracted, but at the current scale keeping an explicit switch is simpler.
- There is currently no built-in system prompt, event bus, interactive REPL, or background daemon.
