# dora

[![English](https://img.shields.io/badge/Language-English-blue.svg)](README.md)
[![简体中文](https://img.shields.io/badge/语言-简体中文-red.svg)](README.zh-CN.md)

`dora` 是一个小巧、模块化的 Go 语言 LLM Agent 内核。其核心是一个循环和两个接口：`Model` 和 `Tool`。

使用内建 provider 时无需配置文件：设置所选 provider 的 API 密钥（例如 `DEEPSEEK_API_KEY`），并可通过按能力（capability）区分的 `policy` 设置选择模型（例如 `DORA_POLICY_TEXT_PROVIDER=deepseek`），即可立即运行。

有关模块边界、依赖、接口和运行时流程，请参阅 [`docs/architecture.md`](docs/architecture.md)。

[DoraBar](https://github.com/lgxz/DoraBar) 是 dora 的配套 macOS 托盘/菜单栏应用，使用 Swift 编写。它提供了轻量级的菜单栏界面，用于与 dora CLI 交互。

## 基准测试结果

dora 在 [terminal-bench-2.1](https://github.com/harbor-framework/terminal-bench-2.1)（89 个任务）上进行评估。下表记录了随着 dora 的演进，各次评估运行的平均通过率：

### deepseek-v4-flash（thinking 关闭）

| 运行 | 日期 | 平均值 | 通过数 | 备注 |
|-----|------|------|--------|-------|
| 1 | 2026-08-12 | 0.573 | 51/89 | 基线 |
| 2 | 2026-08-13 | 0.483 | 43/89 | 受速率限制影响 |
| 3 | 2026-08-13 | 0.596 | 53/89 |  |
| 4 | 2026-08-14 | 0.607 | 54/89 | 后台任务支持 |
| 5 | 2026-08-14 | 0.618 | 55/89 | 系统提示词 |
| 6 | 2026-08-14 | 0.685 | 61/89 | read/write/edit 工具 |

### deepseek-v4-pro（thinking：high）

| 运行 | 日期 | 平均值 | 备注 |
|-----|------|------|--------|
| 1 | 2026-08-28 | 0.787 |  |

https://hub.harborframework.com/jobs/747f9a67-ed3a-498b-a70e-6786a70eb8a5

## 范围

内核支持可选的模型流式事件，同时保持其基线 `Model` 接口为同步。工具调用被刻意安排为仅在当前模型响应完成后才执行。一个响应中的多个工具调用会并发执行，但会修改文件或进程的工具（`write`、`edit`、`bash`、`powershell`）实现了可选的 `ExclusiveTool` 接口，会单独按模型书写的顺序依次运行，因此依赖前一个调用的调用能看到它的效果。结果按返回顺序保存。内核没有内置的内存、策略引擎、中间件或提供商 SDK。

## CLI

### 安装

在 macOS 或 Linux 上，使用 curl 安装最新版本：

```sh
curl -LsSf https://github.com/lgxz/dora/releases/latest/download/dora-installer.sh | sh
```

当 curl 不可用时使用 wget：

```sh
wget -qO- https://github.com/lgxz/dora/releases/latest/download/dora-installer.sh | sh
```

在 Windows 上，使用 PowerShell：

```powershell
powershell -ExecutionPolicy Bypass -c "irm https://github.com/lgxz/dora/releases/latest/download/dora-installer.ps1 | iex"
```

安装程序会为当前操作系统和架构下载对应的归档文件，并对照 release 的 SHA-256 校验和进行验证，默认将 `dora` 安装到 `$HOME/.local/bin`。设置 `DORA_INSTALL_DIR` 可选择其他目录：

```sh
curl -LsSf https://github.com/lgxz/dora/releases/latest/download/dora-installer.sh \
  | env DORA_INSTALL_DIR=/usr/local/bin sh
```

通过使用带标签的安装程序 URL 来安装特定版本。每个安装程序都绑定到包含它的那个 release：

```sh
curl -LsSf https://github.com/lgxz/dora/releases/download/v0.1.0/dora-installer.sh | sh
```

运行 `dora --version` 可查看已安装版本的版本号、源码提交和构建日期。包含自更新支持的独立版 release 可以自行更新：

```sh
dora -update
```

更新程序会检查最新的稳定版 GitHub release，对照 `checksums.txt` 验证其归档文件，校验下载的可执行文件，并在失败时回滚替换当前二进制文件。Go 构建、手动安装的归档以及包管理器安装均不受管理；请通过其原始的安装方式进行升级。对旧安装重新运行一次最新安装程序即可启用自更新。Release 归档和校验和仍可在 GitHub Releases 页面获取，用于手动安装和验证。

要使用最新 release 替换不受管理或开发版构建（例如通过 `make install` 安装的），可跳过独立安装标记和版本检查：

```sh
dora -update --force
```

用法：

```sh
cat notes.md | dora Summarize the following content | mdcat
```

### 从源码构建

构建 Dora 需要 Go 1.25 或更高版本。CI 同时检查最低支持的 Go 1.25 行和当前的 Go 1.26 行；发布二进制文件使用最新的 Go 1.26 patch release。

带调试信息构建命令：

```sh
make build
```

二进制文件写入 `build/dora`（在 Windows 上为 `build/dora.exe`）。如需不含符号和 DWARF 调试数据的小型发布二进制文件，请使用：

```sh
make release
```

release 目标使用 `-trimpath`，去除调试数据，并嵌入版本号、提交和构建日期，输出到 `dist/dora`（在 Windows 上为 `dist/dora.exe`）。需要时可覆盖 `VERSION`、`COMMIT` 或 `BUILD_DATE`。在 Windows 上，当 `make` 不可用时，可直接运行等效的 Go 命令并将输出路径设为 `dist/dora.exe`。

要构建发布二进制文件并将其安装到 `$(PREFIX)/bin/dora`（默认 `$HOME/.local/bin/dora`）：

```sh
make install
```

`install` 依赖 `release`，并在需要时创建 `$(PREFIX)/bin`。覆盖 `PREFIX` 可选择其他位置，例如 `make install PREFIX=/usr/local`。

### API 密钥

Dora 根据每个 provider 名称派生 API 密钥环境变量：名称转为大写，非字母数字字符替换为 `_`，再追加 `_API_KEY`。例如 `deepseek`、`trust` 和 `open-router` 分别使用 `DEEPSEEK_API_KEY`、`TRUST_API_KEY` 和 `OPEN_ROUTER_API_KEY`。非空的真实进程环境变量会覆盖配置文件 `env` 下的同名值。配置值只作为 Dora 内部 fallback，不会导出给子进程。

模型选择由按能力（capability）区分的 `policy` 驱动，而非单一模型选择器。每种能力（`text` 与 `image`）都有一个可选的 `{provider, profile}` 对；任一字段留空即回退为自动（`auto`）选择。policy 字段对应的环境变量覆盖为 `DORA_POLICY_<CAPABILITY>_<FIELD>`（例如 `DORA_POLICY_TEXT_PROVIDER`、`DORA_POLICY_TEXT_PROFILE`、`DORA_POLICY_IMAGE_PROVIDER`、`DORA_POLICY_IMAGE_PROFILE`）；环境变量优先于配置文件。

首次使用时可以运行交互式配置：

```sh
dora --setup
```

setup 会选择一个内建 provider，把 API key 保存到配置文件的 `env`，并设置 `policy.text.provider`。具体模型 profile 可选；直接回车会保持自动选择。现有配置字段和注释会被保留。配置写入 `~/.dora/config.yaml`（或 `--config` 指定的路径），Unix 上仅允许文件所有者访问。真实进程环境变量仍优先于配置文件中的值。

macOS / Linux，临时（仅当前终端）：

```sh
export TRUST_API_KEY="sk-..."
export DORA_POLICY_TEXT_PROVIDER="trust"
export DORA_POLICY_TEXT_PROFILE="deepseek-v4-flash"
```

macOS / Linux，永久：将上面的 `export` 行追加到 `~/.zshrc`（zsh）或 `~/.bashrc`（bash），然后重新加载：

```sh
source ~/.zshrc
```

Windows PowerShell，临时（仅当前会话）：

```powershell
$env:TRUST_API_KEY = "sk-..."
$env:DORA_POLICY_TEXT_PROVIDER = "trust"
$env:DORA_POLICY_TEXT_PROFILE = "deepseek-v4-flash"
```

Windows PowerShell，永久（对当前用户持久生效）：

```powershell
[Environment]::SetEnvironmentVariable("TRUST_API_KEY", "sk-...", "User")
[Environment]::SetEnvironmentVariable("DORA_POLICY_TEXT_PROVIDER", "trust", "User")
[Environment]::SetEnvironmentVariable("DORA_POLICY_TEXT_PROFILE", "deepseek-v4-flash", "User")
```

Windows CMD，临时（仅当前会话）：

```cmd
set TRUST_API_KEY=sk-...
set DORA_POLICY_TEXT_PROVIDER=trust
set DORA_POLICY_TEXT_PROFILE=deepseek-v4-flash
```

当 policy 字段回退为 `auto` 时，Dora 按 catalog 顺序遍历——先按 provider 的列出顺序，再按该 provider 中 profiles 的列出顺序——并选中第一个「具有非空 API key（即可用）且满足能力约束」的条目。因此 catalog 顺序即优先级。没有 API key 的 provider 被视为不可用，并在选择时被跳过。无需认证的本地端点（如 Ollama）同样需要设置一个任意非空占位 API key 才能被选中。没有配置文件且没有 key 时，仍使用内建的 DeepSeek 默认值。

要自定义默认值，请创建 `~/.dora/config.yaml`。Dora 在每个操作系统（包括 macOS）上都使用这个 `~/.dora/` 布局。设置 `DORA_HOME` 环境变量为绝对路径可覆盖 home 目录。你也可以把文件放在任何位置并通过 `--config path/to/config.yaml` 指定；显式请求的文件必须存在。

```yaml
env:
  DEEPSEEK_API_KEY: sk-...
```

这个最小配置直接使用内嵌的 DeepSeek catalog，并自动选择第一个可用的文本模型。将其替换为 `TRUST_API_KEY` 会自动选择 Trust。如果同时配置两个 key，则需要显式设置 `policy.text`：

```yaml
env:
  DEEPSEEK_API_KEY: sk-deepseek...
  TRUST_API_KEY: sk-trust...
policy:
  text:
    provider: trust
    profile: deepseek-v4-flash
```

嵌入式 provider catalog 为 `deepseek`、`trust` 和 `openrouter` 提供内建 `base_url`，因此同名 catalog 项可以省略该字段。模型始终显式列在 `providers[].profiles` 中。每项的 `name` 是由 `policy.*.profile` 选择的唯一 profile 名称；`model` 是发送给 provider 的模型标识。省略 `model` 时默认等于 `name`，多个 profile 可以使用同一个模型并配置不同参数。每个 profile 通过 `capabilities` 声明其能力，例如 `capabilities: [text]` 或 `capabilities: [text, image_input]`。可在 provider 或 model 层设置 `api: responses` 使用 Responses API。两种 API 都始终使用 SSE 流式输出。Responses 工具循环在本地重放类型化输出项，不依赖服务端的响应存储。

设置 `OPENROUTER_API_KEY` 后，内建的 `openrouter/auto` profile 会自动参与文本和图片选模。catalog 顺序仍为 DeepSeek、Trust、OpenRouter，因此前面的可用 provider 仍优先。也可以通过 `-m openrouter/auto` 或 policy 显式选择。内建的 `openrouter/ox-alpha` profile 会直接使用 `stealth/ox-alpha`。

### 第三方 OpenAI 兼容提供商

要使用任何支持 OpenAI Chat Completions 协议的其他第三方提供商（例如 Ollama、LM Studio、vLLM、Groq、Together 或自托管端点），请将它添加到 `providers`，设置 `base_url` 和 profiles。Chat Completions 端点为 `base_url + "/chat/completions"`，因此 `base_url` 应为提供商的 `/v1`（或等效）根路径。

对于无需认证的自托管 Ollama 端点，也需设置任意非空占位 API key（例如 `export OLLAMA_API_KEY=local`，或在配置 `env` 段下使用相同名称），否则该 provider 会被视为不可用并在选择时被跳过：

```yaml
providers:
  - name: ollama
    base_url: http://localhost:11434/v1
    profiles:
      - name: llama3.1
env:
  OLLAMA_API_KEY: local
```

若要用显式 catalog 替换 OpenRouter 内建的 `auto` profile，可在配置中重新列出该 provider；用户 profile 列表会整体替换内建列表：

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

即使是无需认证的本地端点，也需要一个非空的 API key（任意占位值即可）才能被选中。如果两个 provider 名称归一化后得到相同的环境变量，配置校验会报错。包含 key 的配置文件属于敏感信息，应妥善保护。

使用 `max_tokens`、`max_output_tokens` 和 `temperature` 控制普通响应的输出预算、模型硬输出容量和采样：

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

`max_tokens` 限制普通响应生成的 token 数量，默认为 32768。它在线上以 `max_tokens` 形式发送给 `chat_completions` API（当 profile 设置 `use_max_completion_tokens: true` 时则以 `max_completion_tokens` 发送），以 `max_output_tokens` 形式发送给 `responses` API；显式设置为 `0` 表示"无显式上限"并按原样传递。`max_output_tokens` 是可选且必须为正数的模型硬输出容量，没有默认值；它会同时裁剪 `max_tokens` 和单次请求设置的输出上限。`temperature` 没有默认值：省略时不会发送任何值，提供商使用自己的默认采样。它接受 `[0, 2]` 范围内的值。由于某些推理模型和工具调用模型会忽略或拒绝非默认温度，请将 `temperature` 视为尽力而为。这些键属于 model catalog，没有对应的命令行标志。

`context_window` 属于 model profile，表示模型以 token 为单位的上下文容量。默认值为 1048576，配置值必须为正数。当提供商返回 usage 时，Dora 使用上一次调用的准确 `total_tokens`，再加上该响应之后产生的工具结果 token 估算；没有 usage 时则估算完整消息历史和工具 schema。提供商中立的估算规则约为每四个 ASCII 字节或每个非 ASCII 字符一个 token，并包含少量消息 framing 开销；该估算不估算视觉 token。当包含输出预留在内的预测用量达到上下文窗口的 80% 时，Dora 会请当前模型对对话的中间部分生成摘要。原始任务（最多占窗口的 5%）和最近的完整工具轮次（最多占 10%）会原文保留在摘要两侧，只有两者之间的内容被替换为不超过窗口 20% 的语义摘要，并进一步受 profile 可选的 `max_output_tokens` 裁剪。这个有效目标会作为单次请求的输出上限，覆盖普通请求的 `max_tokens`，避免提供商较小的默认值静默限制压缩。摘要调用不携带工具或 continuation。因输出上限被截断的摘要即使有内容也不会被采用，而是请求更短的摘要重试一次。当历史留给摘要的窗口空间不足时，超大的工具结果只会在摘要请求中被缩短，输出预算也会缩减到剩余空间。用于持久化的完整 Turn 保持不变，摘要失败时也不会回退到删除或本地截断历史。

上述预测只是启发式估算，因此提供商仍可能因请求过大而拒绝。Dora 能识别这种拒绝（HTTP 400/413/422 且错误码或消息表明超出上下文长度，或流中的 error 事件），会立即按窗口已满的假设进行压缩，并重试该请求一次。若重试仍失败，或该轮刚刚压缩过，则运行以提供商的错误结束。

`thinking` 控制模型的"思考模式"推理强度。将其设置为 `off`、`minimal`、`low`、`medium`、`high`、`xhigh` 或 `max` 之一。它没有默认值：省略时不会发送任何值，提供商使用自己的推理默认值。
不同提供商的支持情况各异，不支持的值会被静默忽略而不是报错：

- **openai**：在 Responses API 上会发送全部值（`off` 变为 `none`）；在 Chat Completions 上 `minimal`–`max` 会以 `reasoning_effort` 发送，但 `off` 不受支持（gpt-5 的下限是 `minimal`），并会被忽略。
- **deepseek**：在两个 API 上都会发送 `low` 到 `max`，`minimal` 不受支持并被忽略，而 `off` 在 Chat Completions 上以 `thinking.type: disabled` 发送，在 Responses 上以 `reasoning.effort: none` 发送。
- **trust**：在两个 API 上都按尽力而为的方式处理，类似 OpenAI。
- **openrouter**：在 Chat Completions 上将 `minimal`–`max` 作为 `reasoning_effort` 发送，并将 `off` 作为 `reasoning_effort: none` 发送。

由于一个设置可能被直接丢弃，请将 `thinking` 视为提示而非保证。对于一次性调用，`--thinking` 会用 `off`、`minimal`、`low`、`medium`、`high`、`xhigh` 或 `max` 之一覆盖所选模型的设置：

```sh
./dora --thinking high "Solve a hard problem"
```

`preserve_thinking` 是 profile 级开关（默认关闭），面向 Chat Completions 上的推理模型。开启后，此前工具调用轮次捕获的推理会回传到对应的 assistant 历史消息上：普通推理使用 `reasoning_content`；结构化 `reasoning_details` 会保持顺序和语义保存在 provider continuation 中，并在存在时优先回放。DeepSeek 和 OpenRouter 的内建 profile 已开启；忽略或拒绝回传推理的提供商（以及像 Qwen/DashScope 这样要求剔除的）保持关闭。

一次调用中，`-m`/`--model` 可以用 `PROVIDER/PROFILE` 覆盖配置的对话模型，按名称选择 provider 以及可选的 profile。只写 `PROVIDER`（或带结尾斜杠）会选择该 provider 的默认 profile（第一个满足所需能力的 profile）。它不会修改配置文件：

```sh
./dora -m trust/deepseek-v4-flash "Explain this repository"
./dora -m deepseek "Explain this repository"
```

`-m` 只影响文本对话模型，不影响图片模型和技能模型。如果名称在该 provider 下没有匹配的 profile，则把它当作未注册的模型 ID 直接使用。只有第一个 `/` 用来分隔 provider，因此 `-m openrouter/vendor/model-name` 发送的是 `vendor/model-name`。这个临时文本 profile 使用该 provider 的连接和 API 设置、`max_tokens: 32768` 和 `context_window: 1048576`，不会继承其他 profile 的参数。`--thinking` 仍然适用。

已有的 profile 始终优先，包括它们的能力限制。未知的 provider、缺失的 API key 以及不兼容的已有 profile，仍然会得到 `router: no model satisfies the constraints`。未知的模型 ID（包括拼错的 profile 名称）交给提供商去拒绝。这个回退只适用于 `-m`/`--model`；policy 字段及其环境变量覆盖仍然要求已配置的 profile。

Dora 默认每个片段最多运行 256 轮模型-工具循环。请保留这一安全机制，但在工具工作流异常冗长时进行调整：

```yaml
agent:
  max_rounds: 96
```

模型响应必须明确给出正常的结束原因。空的最终答案，或缺失/未知的结束原因，都会让该 turn 失败，而不会被悄悄当作完成。输出被截断时，即使返回的工具调用参数看起来完整，也绝不会执行这些工具调用。

对于普通 Agent 调用，Dora 会从最近一次完整交互处重试一次输出受限的响应，并把实际请求的输出预算翻倍（例如 32768 变为 65536）。它保留原有历史和 continuation，不假设提供商能够续写被截断的推理。重试受模型声明的输出容量、可用上下文以及可选的恢复上限约束。如果原始预算未知、无法再增加，或重试同样被截断，该 turn 会明确失败。取消和截止时间仍然生效。

```yaml
agent:
  output_limit_recovery:
    max_retries: 1          # 0 表示关闭输出受限恢复
    max_output_tokens: 65536 # 可选的恢复上限
```

这个限制独立于传输层重试和 `max_rounds`。profile 的 `max_output_tokens` 声明的是硬容量，不会提高普通的 `max_tokens` 预算。提高恢复预算可能增加延迟和成本。

使用 `--max-rounds` 为一次调用覆盖轮数上限：

```sh
./dora --max-rounds 96 "Complete a long task"
```

当 stdin 和 stderr 都连接到终端且达到限制时，Dora 会询问是否继续到下一个片段。确认后将从已完成的工具输出处恢复，而不重放已完成的工作。拒绝则正常停止，但不会持久化这个未完成的 turn。使用管道或重定向 I/O 时，Dora 不会提示，而是返回 `maximum rounds exceeded`。

每个 Agent 都持有一条不可变的系统提示词。二进制内置了一份默认提示词（涵盖诸如"宣布任务完成前先按请求的字面要求核对结果"之类的工作习惯）；配置非空的 `agent.system_prompt` 会整体替换内置默认：

```yaml
agent:
  system_prompt: |
    你是 Acme 部署团队的终端助手。
    优先使用 /opt/acme 下的部署脚本。
```

Dora 会在默认提示词和自定义提示词之后追加一个 `runtime_environment` 块，包含操作系统、架构以及 Agent 的本地启动日期（不含时间）。这份快照在 Agent 的整个生命周期（包括子任务）内保持不变，在启动时捕获。长时间运行的 Agent 不会在跨午夜时刷新日期。

运行一次性提示，或将指令与管道输入结合：

```sh
export DEEPSEEK_API_KEY="..."
./dora "Explain this repository"
git diff | ./dora "Review this change"
```

进度会以一个小巧的 Dora 个性显示在 stderr 上，而最终答案保持在 stdout。当只需要答案时使用 `--quiet` 或 `-q`：

```sh
./dora --quiet "Explain this repository"
```

当 stdout 是终端时，Dora 以纯文本打印最终答案。重定向或管道的 stdout 相同，为脚本保持稳定输出：

```sh
./dora "Write release notes"
./dora "Write release notes" > release-notes.md
```

进度颜色默认使用 `--color=auto`：当 stderr 是终端且未设置 `NO_COLOR` 时启用。使用 `--color=always` 可在 stderr 被重定向时仍保留 ANSI 颜色，使用 `--color=never` 可将其关闭。显式颜色模式会覆盖自动终端和环境检测；所有模式下进度仍显示在 stderr 上。瞬时的 `Thinking...` 占位符仅在 stderr 是终端时显示；重定向的进度会省略它，只保留模型、工具、信息和错误等实质内容。执行之前，进度会以 `Model PROVIDER/PROFILE · thinking=VALUE` 标明解析后的对话模型；这些是经过 policy、自动可用性过滤和命令行覆盖之后的有效设置。`thinking=default` 表示没有向提供商显式发送 thinking 值。`--quiet` 会连同其它进度输出一并隐藏这一行。

推理模型会在最终答案前输出思维链。由于向终端流式输出思维链会在慢速终端上拖慢执行，Dora 默认将其隐藏；传入 `--reasoning` 可以暗色样式实时显示，在终端中替代 `Thinking...` 占位行。最终答案仍在 stdout 上另起一行输出，`--quiet` 会连同其它进度一并隐藏推理显示。

当提供商报告 token 用量时，Dora 会记录每个完成的模型轮次的用量，并在可用时包含缓存和推理 token 的明细。终端渲染器不打印用量。每轮用量和最终响应用量保存在当前会话中，并由 history 工具返回；没有 `--session` 时，会话在内存中，随 Dora 退出而消失。

如需机器可读的父 turn 记录，传入 `--trace-file PATH`。Dora 会写出与提供商无关的 JSON 记录，包含 system 和 user 消息、每个已完成的 assistant/tool 轮次（包括推理、工具输入、结果、图片和每次调用的用量），以及产生了最终响应时的最终响应。Trace schema **2** 还包含 `status`、`error`、`attempts` 和 `total_usage`。每次模型尝试都会记录其结束原因、提供商原始原因、实际输出预算（未知时为零）、恢复序号、处置方式、推理、文本、工具参数以及报告的用量。被丢弃的调用和压缩调用也包含在内。最终推理会被保留。`attempts` 只应累加一次：被采纳的响应同样出现在 `rounds`/`final` 中，不能重复计数。缺失的用量视为未知；总计只累加已报告的数量。子任务和图片工具的模型调用不会展开到父 trace 中。Harbor 适配器现在要求 schema 2，schema 1 会被拒绝。格式错误的提供商工具参数会以文本形式保留，不会妨碍记录失败的 turn。父目录必须已经存在，文件以 `0600` 权限创建。该选项仅支持一次性运行。Terminal-Bench 集成可以把这份稳定的父 turn 记录转换为基准测试的 ATIF 格式，而无需让核心 Agent 依赖 Harbor。

使用 `--workdir` 可以指定工具解析相对路径时使用的基准目录：

```sh
./dora --workdir /path/to/project "运行测试并检查失败原因"
```

该目录必须已经存在。Dora 会将它解析为绝对路径，但不会修改进程的当前目录，因此并发 Agent Run 可以安全地使用不同基准。它适用于命令工具，以及 `read`、`write`、`edit`、`grep`、`glob` 和 `view_image` 的相对路径；绝对路径不受影响。配置文件和 `--session` 路径仍相对于进程当前目录解析。`--workdir` 只是路径参照，并不是文件系统沙箱或额外的权限边界。

### Azure OpenAI

使用其 [v1 API](https://learn.microsoft.com/en-us/azure/foundry/openai/api-version-lifecycle) 配置 Azure OpenAI。请显式把它添加到配置中：Azure 没有默认的资源端点或部署 catalog。将 `base_url` 设置为以 `/openai/v1` 结尾的资源 URL，并将 `model` 设置为你的 Azure 部署名称。

```yaml
providers:
  - name: azure
    base_url: https://YOUR-RESOURCE.openai.azure.com/openai/v1
    profiles:
      - name: gpt
        model: YOUR-DEPLOYMENT
        use_max_completion_tokens: true
        capabilities: [text]
        max_tokens: 32768
policy:
  text:
    provider: azure
    profile: gpt
```

在环境变量或配置的 `env` 中设置 `AZURE_API_KEY`。对于要求使用该参数的模型，设置 `use_max_completion_tokens: true`。这个 profile 设置适用于任何 provider 名称，默认为 false。它会在 Chat Completions 上把 `max_tokens` 预算以 `max_completion_tokens` 发送，包括压缩和输出受限恢复所用的单次请求上限；对 Responses API 无效。Azure 也接受 `api: responses`，此时发送 `max_output_tokens`。该集成面向 v1 端点；需要 `api-version` 查询参数的旧式部署 URL 不受支持。

### 模型请求重试

瞬时的模型故障，包括收到部分文本或推理后连接被重置，以及流式 JSON 不完整（`unexpected end of JSON input`），会重试当前的模型请求，总共最多六次尝试。其他 JSON 语法和类型错误不会重试。五次等待分别为 2、4、8、16 和 30 秒，外加最多 50% 的随机抖动；提供商给出的重试延迟优先，但最多采用两分钟；要求等待更久时，请求会立即失败并给出说明。速率限制失败保留其独立的五次尝试策略和提供商指定的延迟。

除非设置了 `--quiet`，重试进度会在 stderr 上报告下一次尝试、延迟和错误。取消会中断等待。失败的尝试绝不会提交部分响应，也不会执行其中的工具调用；重试复用当前请求，不会重放已完成的工具轮次。已经显示的文本或推理无法撤回，重试后可能再次出现。只有成功的完整响应才会进入 turn 历史。

### 会话

传入一个会话文件（JSON Lines，惯例扩展名 `.jsonl`），可以跨多次 CLI 调用保留 turns：

```sh
./dora -s ./system-status.jsonl "Analyze this machine's system status"
./dora -s ./system-status.jsonl "Continue with the busiest processes"
```

每次调用都是一个全新且独立的 turn，历史消息不会自动装入模型上下文。Dora 始终提供 `history` 工具，包括第一个 turn 之前：模型可以用 `list` 列出 turns 及各自的状态、错误、round 数量和最终响应 usage，再用 `turn_id`、`offset`、`limit` 按时间顺序分页 `get` rounds；空会话会返回空列表。一个 round 是一条 assistant tool-call 消息、对应的全部 tool 结果消息及该次模型调用的可选 usage。成功完成的 turn 会原子追加。因达到最大 rounds 而停止的 turn 也会以 `max_rounds` 状态保存，包含其错误和全部已完成的工具 rounds，但没有最终结果和最终响应 usage。Ctrl+C（`SIGINT`）和 `SIGTERM` 造成的取消以 `canceled` 状态保存；Dora 使用单独的短时提交上下文，使取消运行不会同时取消它的会话写入。其他失败的 turn 以 `failed` 状态保存。两者都会保留错误、完整的工具 rounds 以及单独的模型尝试审计记录，包括输出受限的响应。传输故障可能没有响应内容或 usage。确认交互式继续提示会继续使用同一个 Turn，不会保存中间的 `max_rounds` 记录。Provider continuation 只在该 turn 运行期间保留。

使用 GNU `timeout` 时，请在强制终止前留出取消和会话存储的时间，例如：

```sh
timeout --kill-after=10s 300s dora --session session.jsonl "Your task"
```

最初的 `SIGTERM` 请求取消；额外的十秒用于在 `SIGKILL` 之前完成清理。父 turn 存储和每个子任务历史的写入各有五秒预算；后台 Task 的关闭也可能再等待五秒。取消缓慢或子任务历史清理可能需要更长的宽限期。`SIGKILL` 无法被处理，可能丢失当前 turn。只会保留完整的工具 rounds 和可用的模型尝试记录，不保留未完成的流式文本。

指定 `--session` 时，文件是只追加的 JSON Lines 日志：一行 header，随后是 `turn`、`round`、`attempt` 和 `end` 记录，合起来记录 turn 状态和错误、system prompt、用户输入、最终结果、中间工具 rounds、附带的图片引用、round assistant 消息上捕获的推理，以及每次模型调用的 usage JSON、结束元数据、最终推理和报告的总用量。存储的 `GetAttempts` API 与对话 rounds 分开，单独分页读取审计记录。新文件权限为 `0600`。一个会话文件同一时间只属于一个 Dora 进程：第二个进程打开同一文件会报错 “in use”（在支持文件锁的平台上）。旧的 SQLite 和 JSON 会话、`--fresh` 以及自动迁移均不支持（这类文件会被原样拒绝，请新建文件）。崩溃留下的写了一半的最后一行，会在下次打开文件时丢弃。每个完整的工具 round 在 turn 运行期间就会保存，因此崩溃或强制退出时仍会保留已完成的 rounds；这样被打断的 turn 会停留在 `running` 状态，正在进行的那个 round 会丢失。Dora 不会恢复被中断的 turn。省略 `--session`/`-s` 时，Dora 会在当前进程内把会话保存在内存中；这让长驻模式可以保留此前 turns，而普通单次 CLI 调用仍是临时的。Session 文件可能包含命令、工具输出及 token 使用信息，因此请将持久化文件视为敏感内容。

使用 `--config`、`-m`/`--model`、`--thinking`、`--max-rounds` 或 `--no-skills` 可为一次调用覆盖相应的配置。

子任务 turn 保存在同一个文件中，其 `parent_turn_id` 指向主 turn。`history list` 返回所有 turns，包括每个 turn 的最终结果、状态以及子任务的 `parent_turn_id`（主 turn 省略该字段）。它的总数和分页同时统计主 turn 和子 turn，因此相关的 turns 可能出现在不同页。`history get` 保持按 rounds 分页的格式：可以用任意 turn ID 查看该 turn 的工具 rounds。父 turn 的 usage 和 trace 输出不包含子任务的 usage。

Task 完成时只在内存中收集终态 Turn，不等待存储写入。收集到的子任务在一次 prompt 结束后以及会话关闭时保存。后台 Task 始终保留其原始父 turn，即使它在之后的 prompt 期间才结束；迟到的结果在下一次 prompt 结束或会话关闭的边界保存。失败、取消和达到最大轮数的子任务同样会被保存。子任务的写入失败会在清理时报告，但不会改变 Task 的结果。强制终止仍可能丢失尚未保存的子任务记录。

Session 存储会精确保留工具参数的字节，包括格式错误的 JSON。`history get` 把每个工具调用的 `input` 作为包含原始参数文本的字符串返回，有效和无效 JSON 都是如此。对于无效的 UTF-8，还会包含 base64 编码的 `input_bytes`，使原始字节仍然可用。History 输出仍然使用现有的大小限制；结果被截断时请缩小分页范围。

### 技能（Skills）

技能是仅在相关内容时由模型加载的本地指令包。每个技能都是一个包含 `SKILL.md` 的目录，带有严格的 YAML front matter：

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

默认情况下，Dora 会在 `~/.dora/skills`（或 `DORA_HOME/skills`）之后接着在 `~/.agents/skills/` 发现技能，与活动的 `config.yaml` 路径无关。每个默认目录仅当其作为目录存在时才会被纳入（缺失的默认目录会被静默跳过）。无需任何配置。

使用 `skills.directories` 可以将默认目录替换为特定的父目录集合：

```yaml
skills:
  directories:
    - /absolute/path/to/additional-skills
```

每个配置的路径必须是绝对路径或 `~/` 开头。相对路径会被拒绝。配置目录会转换为绝对形式并去重；它们完全按所列内容使用，不会与默认目录合并。使用 `--no-skills` 可为一次调用禁用所有技能来源；它优先于 `skills.directories`。

Dora 仅在 `skill` 工具 schema 中公布技能名称和描述。当模型调用该工具时，会返回技能的绝对目录和完整的 `SKILL.md`，从而允许指令引用诸如 `scripts/check.sh` 之类的文件。技能工具从不执行这些文件；执行仍然需要一个已启用的工具（如 Bash）。名称必须包含小写字母、数字和连字符，并且必须与其目录名匹配。会拒绝重复的名称。缺失或为空的默认目录只会让该工具保持禁用；畸形的已发现技能和缺失的显式配置目录则属于错误。

## 发布（Releasing）

推送语义化版本标签会运行发布工作流：

```sh
git tag v0.1.0
git push origin v0.1.0
```

工作流会运行完整的验证，渲染绑定到该标签的安装程序，并使用 GoReleaser 为 Linux、macOS 和 Windows（包括 amd64 和 arm64）发布静态归档。它还会发布 `checksums.txt`；公共仓库会收到 GitHub 构建来源证明。不符合语义化版本语法的标签会在发布前失败。

### 命令工具

命令工具使用平台感知的自动默认值：Bash 在 Linux 和 macOS 上启用，而 PowerShell 在 Windows 上启用。另一个命令工具即使其可执行文件在 `PATH` 上也会被禁用。省略 `enabled` 以使用该策略，或显式设置以覆盖平台默认值：

```yaml
tools:
  bash:
    enabled: false
  powershell:
    enabled: true
  task:
    enabled: false
```

可执行文件不存在的自动工具会被跳过。用 `enabled: true` 显式启用的工具必须存在于 `PATH` 上，否则 Dora 会报告错误。目前发现机制仅检查可执行文件是否存在；它不会启动 shell 去探测其运行时环境。

Bash 工具在 `--workdir` 指定的目录下运行 `bash -c`；省略该参数时使用 Dora 进程的当前目录。当模型需要其他目录时，可以在命令内部使用 `cd`。该工具以 JSON 形式向模型返回退出码、stdout 和 stderr。此工具授予模型与 `dora` 进程相同的文件系统和进程权限，因此除非你信任 Dora 运行的环境，否则请禁用它。

独立的 `powershell` 工具优先使用 PowerShell Core（`pwsh`），并在不可用时回退到 Windows PowerShell（`powershell.exe`）。如果两个工具都被显式启用，它们会分开暴露，以便区分各自的命令语法。

PowerShell 使用相同的工作目录规则，需要时可在命令内部使用 `Set-Location`。

两个命令工具都使用同一个 `wait_seconds` 参数。它是命令在被转入后台之前最多等待其结束的秒数；转入后台后命令继续运行（不会被终止），Dora 返回一个 `job_id`，job 工具可以用它来查看该命令。后台进程在 Dora 自身退出时同样不会被终止——它们会在会话结束后继续运行，需要时请用 job 工具的 kill 操作显式停止。在 Unix 上，kill 会终止命令的进程组；在 Windows 上，则终止进程树。这涵盖了诸如 `timeout 240 python3 ...` 之类的普通后代进程。在命令结果、转入后台的结果和 job 轮询中，stdout 和 stderr 各自每次结果上限为 32 KiB（保留头部和尾部，并以标记注明原始大小）；更大的输出请重定向到文件再查看。省略时（默认）Dora 等待 10 秒，取值 `0` 表示立即转入后台：

```json
{
  "command": "go build ./...",
  "wait_seconds": 300
}
```

命令作业的 ID 以来源工具为前缀，并各自独立计数，例如 `bash_0` 和 `powershell_0`。公开的作业结果不包含单独的作业类型字段；由 ID 标识来源。

## 独立任务

`task` 工具默认启用。它接收一条完整、自包含的 `instruction`，使用同一个 Agent 和模型在全新的 `Turn` 中执行，并将该 turn 的最终文本返回给父对话。新 turn 不会继承父 turn 的消息或 provider continuation。它使用同一个不可变的 Agent system prompt，并可使用父 Agent 的其他所有工具，但 `task` 本身会同时从工具定义和可执行工具集合中移除，以防递归委派。

同一模型响应中的多个 Task 调用并发执行（`task` 不是独占工具）。没有单独的 Task 并发限制。子任务的进度不会发送到终端 Observer，只渲染父级消息以及 Task 完成摘要和耗时。

当父级应立即继续而不等待这个独立 turn 时，将 `background` 设为 `true`：

```json
{
  "instruction": "Run the full test suite and summarize failures",
  "background": true
}
```

结果会立即包含一个 `task_N` 作业 ID。使用同一个 `job` 工具来列出、轮询或终止它；`job.poll.wait_seconds` 控制该次轮询的等待时长，而 Task 本身没有耗时估计。已完成的 Task 结果会保留，可重复轮询。与后台 shell 进程不同，后台 Task 运行在 Dora 内部，无法在进程退出后存活。在会话正常关闭时，Dora 会取消剩余的 Task，并最多等待五秒让它们返回，然后在关闭存储之前保存收集到的历史。超过这个宽限期仍忽略取消的 Task 可能丢失其历史。它们的嵌套进度始终保持隐藏，包括在后台运行时。终止 Task 会协作式地取消其上下文。作业在嵌套运行真正返回之前一直报告 `cancelling`，之后才报告 `killed`；因此忽略上下文取消的组件可能延迟终止。

Task 的上下文隔离不是安全沙箱。父子运行共享进程、当前工作目录、文件系统、权限、模型客户端和具体工具实例。不需要委派时可以关闭：

```yaml
tools:
  task:
    enabled: false
```

## 图像理解

Dora 将视觉能力视为 model profile 的能力（capability）。能够理解图像的 profile 声明 `capabilities: [text, image_input]`；纯文本模型声明 `capabilities: [text]`。CLI 不提供覆盖或直接附加图片的标志。

当模型想要查看图像时，它会调用始终注册的 `view_image` 工具。该工具接受本地 `path` 或远程 `url`，并以 `image_input` 能力约束临时选择一个视觉模型来识别图片，返回图片的文字描述。图片本身不会进入主模型的上下文——只有返回的描述才会。

Dora 将每个本地图像文件限制为 4 MiB，并拒绝非图像文件。当 `view_image` 调用指向缺失、非图像或过大的文件时，该工具会向模型报告错误，以便其更正路径。如果没有任何 catalog 项声明 `image_input`，`view_image` 调用会报告没有可用的视觉模型。
