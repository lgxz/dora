# dora

[![English](https://img.shields.io/badge/Language-English-blue.svg)](README.md)
[![简体中文](https://img.shields.io/badge/语言-简体中文-red.svg)](README.zh-CN.md)

`dora` 是一个小巧、模块化的 Go 语言 LLM Agent 内核。其核心是一个循环和两个接口：`Model` 和 `Tool`。

使用内建 provider 时无需配置文件：设置所选 provider 的 API 密钥（例如 `DEEPSEEK_API_KEY`），并可通过按能力（capability）区分的 `policy` 设置选择模型（例如 `DORA_POLICY_TEXT_PROVIDER=deepseek`），即可立即运行。

有关模块边界、依赖、接口和运行时流程，请参阅 [`docs/architecture.md`](docs/architecture.md)。

[DoraBar](https://github.com/lgxz/DoraBar) 是 dora 的配套 macOS 托盘/菜单栏应用，使用 Swift 编写。它提供了轻量级的菜单栏界面，用于与 dora CLI 交互。

## 基准测试结果

dora 在 [terminal-bench-2.1](https://github.com/harbor-framework/terminal-bench-2.1)（89 个任务）上使用 `deepseek-v4-flash` 模型进行评估。下表记录了随着 dora 的演进，各次评估运行的平均通过率：

| 运行 | 日期 | 平均值 | 通过数 | 备注 |
|-----|------|------|--------|-------|
| 1 | 2026-08-12 | 0.573 | 51/89 | 基线 |
| 2 | 2026-08-13 | 0.483 | 43/89 | 受速率限制影响 |
| 3 | 2026-08-13 | 0.596 | 53/89 |  |
| 4 | 2026-08-14 | 0.607 | 54/89 | 后台任务支持 |
| 5 | 2026-08-14 | 0.618 | 55/89 | 系统提示词 |
| 6 | 2026-08-14 | 0.685 | 61/89 | read/write/edit 工具 |

## 范围

内核支持可选的模型流式事件，同时保持其基线 `Model` 接口为同步。工具调用被刻意安排为仅在当前模型响应完成后才执行，并且一个响应中的多个工具调用会并发执行（结果按返回顺序输出）。内核没有内置的内存、策略引擎、中间件或提供商 SDK。

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

对于无需认证的本地端点，同时省略真实环境变量和配置 fallback。如果两个 provider 名称归一化后得到相同的环境变量，配置校验会报错。包含 key 的配置文件属于敏感信息，应妥善保护。

使用 `max_tokens` 和 `temperature` 控制每个响应的输出预算和采样：

```yaml
providers:
  - name: openrouter
    base_url: https://openrouter.ai/api/v1
    profiles:
      - name: balanced
        model: openrouter/auto
        max_tokens: 32768
        temperature: 0.7
policy:
  text:
    provider: openrouter
    profile: balanced
```

`max_tokens` 限制模型在一次响应中生成的 token 数量，默认为 32768。它在线上以 `max_tokens` 形式发送给 `chat_completions` API，以 `max_output_tokens` 形式发送给 `responses` API；显式设置为 `0` 表示"无显式上限"并按原样传递。`temperature` 没有默认值：省略时不会发送任何值，提供商使用自己的默认采样。它接受 `[0, 2]` 范围内的值。由于某些推理模型和工具调用模型会忽略或拒绝非默认温度，请将 `temperature` 视为尽力而为。这两个键属于 model catalog，没有对应的命令行标志。

`context_window` 属于 model profile，表示模型以 token 为单位的上下文容量。默认值为 1048576，配置值必须为正数。当提供商返回 usage 时，Dora 使用上一次调用的准确 `total_tokens`，再加上该响应之后产生的工具结果 token 估算；没有 usage 时则估算完整消息历史和工具 schema。提供商中立的估算规则约为每四个 ASCII 字节或每个非 ASCII 字符一个 token，并包含少量消息 framing 开销；该估算不估算视觉 token。当包含输出预留在内的预测用量达到上下文窗口的 80% 时，Dora 会请当前模型生成不超过窗口 20% 的语义摘要，用它替换模型可见历史。摘要调用不携带工具或 continuation。用于持久化的完整 Turn 保持不变，摘要失败时也不会回退到删除或本地截断历史。

`thinking` 控制模型的"思考模式"推理强度。将其设置为 `off`、`minimal`、`low`、`medium` 或 `high` 之一。它没有默认值：省略时不会发送任何值，提供商使用自己的推理默认值。
不同提供商的支持情况各异，不支持的值会被静默忽略而不是报错：

- **openai**：在 Responses API 上会发送 `off`→`none`、`minimal`、`low`、`medium` 和 `high` 全部值；在 Chat Completions 上 `minimal`–`high` 会以 `reasoning_effort` 发送，但 `off` 不受支持（gpt-5 的下限是 `minimal`），并会被忽略。
- **deepseek**：在两个 API 上都会发送 `low`/`medium`/`high`，`minimal` 不受支持并被忽略，而 `off` 在 Chat Completions 上以 `thinking.type: disabled` 发送，在 Responses 上以 `reasoning.effort: none` 发送。
- **trust**：在两个 API 上都按尽力而为的方式处理，类似 OpenAI。
- **openrouter**：在 Chat Completions 上将 `minimal`–`high` 作为 `reasoning_effort` 发送，并将 `off` 作为 `reasoning_effort: none` 发送。

由于一个设置可能被直接丢弃，请将 `thinking` 视为提示而非保证。对于一次性调用，`--thinking` 会用 `off`、`minimal`、`low`、`medium` 或 `high` 之一覆盖所选模型的设置：

```sh
./dora --thinking high "Solve a hard problem"
```

`preserve_thinking` 是 profile 级开关（默认关闭），面向 Chat Completions 上的推理模型。开启后，此前工具调用轮次捕获的推理会回传到对应的 assistant 历史消息上：普通推理使用 `reasoning_content`；结构化 `reasoning_details` 会保持顺序和语义保存在 provider continuation 中，并在存在时优先回放。DeepSeek 和 OpenRouter 的内建 profile 已开启；忽略或拒绝回传推理的提供商（以及像 Qwen/DashScope 这样要求剔除的）保持关闭。

Dora 默认每个片段最多运行 256 轮模型-工具循环。请保留这一安全机制，但在工具工作流异常冗长时进行调整：

```yaml
agent:
  max_rounds: 96
```

使用 `--max-rounds` 为一次调用覆盖它：

```sh
./dora --max-rounds 96 "Complete a long task"
```

当 stdin 和 stderr 都连接到终端且达到限制时，Dora 会询问是否继续到下一个片段。确认后将从已完成的工具输出处恢复，而不重放已完成的工作。拒绝则正常停止，但不会持久化这个未完成的 turn。使用管道或重定向 I/O 时，Dora 不会提示，而是返回 `dora: maximum rounds exceeded`。

每个 Agent 都持有一条不可变的系统提示词。二进制内置了一份默认提示词（涵盖诸如"宣布任务完成前先按请求的字面要求核对结果"之类的工作习惯）；配置非空的 `agent.system_prompt` 会整体替换内置默认：

```yaml
agent:
  system_prompt: |
    你是 Acme 部署团队的终端助手。
    优先使用 /opt/acme 下的部署脚本。
```

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

进度颜色默认使用 `--color=auto`：当 stderr 是终端且未设置 `NO_COLOR` 时启用。使用 `--color=always` 可在 stderr 被重定向时仍保留 ANSI 颜色，使用 `--color=never` 可将其关闭。显式颜色模式会覆盖自动终端和环境检测；所有模式下进度仍显示在 stderr 上。

推理模型会在最终答案前输出思维链。由于向终端流式输出思维链会在慢速终端上拖慢执行，Dora 默认将其隐藏；传入 `--reasoning` 可以暗色样式实时显示，替代 "Thinking..." 占位行。最终答案仍在 stdout 上另起一行输出，`--quiet` 会连同其它进度一并隐藏推理显示。

使用 `--workdir` 可以指定工具解析相对路径时使用的基准目录：

```sh
./dora --workdir /path/to/project "运行测试并检查失败原因"
```

该目录必须已经存在。Dora 会将它解析为绝对路径，但不会修改进程的当前目录，因此并发 Agent Run 可以安全地使用不同基准。它适用于命令工具，以及 `read`、`write`、`edit`、`grep`、`glob` 和 `view_image` 的相对路径；绝对路径不受影响。配置文件和 `--session` 路径仍相对于进程当前目录解析。`--workdir` 只是路径参照，并不是文件系统沙箱或额外的权限边界。

### 会话

传入一个 SQLite 文件，可以跨多次 CLI 调用保留已完成的 turns：

```sh
./dora -s ./system-status.sqlite "Analyze this machine's system status"
./dora -s ./system-status.sqlite "Continue with the busiest processes"
```

每次调用都是一个全新且独立的 turn，历史消息不会自动装入模型上下文。Dora 从第一个 turn 开始就加入 `history` 工具：模型可以用 `list` 列出已保存的 turns 及各自的状态、round 数量和最终响应 usage，再用 `turn_id`、`offset`、`limit` 按时间顺序分页 `get` rounds；空数据库会返回空列表。一个 round 是一条 assistant tool-call 消息、对应的全部 tool 结果消息及该次模型调用的可选 usage。成功完成、达到最大 rounds 或因其他错误失败的 turn 都会一次性原子追加；失败 turn 只保存错误和失败前完整完成的工具 rounds，不保存流式 partial output。Provider continuation 只在当前 turn 运行期间保存在内存中。

指定 `--session` 时，SQLite schema version 6 包含 `turns` 和 `messages` 表，记录 turn 状态和错误、system prompt、用户输入、最终结果、中间工具 rounds、round assistant 消息上捕获的推理，以及每次模型调用的 usage JSON。新文件权限为 `0600`。旧命名 JSON session 格式、`--fresh` 以及自动迁移均不支持（schema version 5 及更早的数据库会被拒绝，请新建文件）。省略 `--session`/`-s` 时，Dora 会为当前进程创建内存 SQLite 数据库；这让长驻模式可以保留此前 turns，而普通单次 CLI 调用仍是临时的。持久化 Session 文件可能包含命令、工具输出及 token 使用信息，因此请将其视为敏感内容。

使用 `--config`、`--thinking`、`--max-rounds` 或 `--no-skills` 可为一次调用覆盖相应的配置。

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
    timeout_seconds: 120
  powershell:
    enabled: true
    timeout_seconds: 120
  task:
    enabled: false
```

可执行文件不存在的自动工具会被跳过。用 `enabled: true` 显式启用的工具必须存在于 `PATH` 上，否则 Dora 会报告错误。目前发现机制仅检查可执行文件是否存在；它不会启动 shell 去探测其运行时环境。

Bash 工具在 `--workdir` 指定的目录下运行 `bash -lc`；省略该参数时使用 Dora 进程的当前目录。当模型需要其他目录时，可以在命令内部使用 `cd`。该工具以 JSON 形式向模型返回退出码、stdout、stderr、超时和截断信息。每个流的输出限制为 1 MiB。此工具授予模型与 `dora` 进程相同的文件系统和进程权限，因此除非你信任 Dora 运行的环境，否则请禁用它。

独立的 `powershell` 工具优先使用 PowerShell Core（`pwsh`），并在不可用时回退到 Windows PowerShell（`powershell.exe`）。如果两个工具都被显式启用，它们会分开暴露，以便区分各自的命令语法。

PowerShell 使用相同的工作目录规则，需要时可在命令内部使用 `Set-Location`。

两个命令工具都接受可选的每次命令超时。它覆盖该次调用的配置默认值，且不能超过 3600 秒：

```json
{
  "command": "go build ./...",
  "timeout_seconds": 300
}
```

省略时，`timeout_seconds` 来自对应的 YAML 工具设置；当该设置为零或缺失时，默认 120 秒。

## 独立任务

`task` 工具默认启用。它接收一条完整、自包含的 `instruction`，使用同一个
Agent 和模型在全新的 Turn 中执行，并将最终文本返回父上下文。新 Turn
不会继承父 Turn 的消息或 provider continuation；它会绑定同一个 Agent
system prompt，并可使用父 Agent 的其他工具，但 `task` 会同时从子运行的
工具声明和执行集合中移除，以防递归委派。

同一模型响应中的多个 Task 调用遵循 Dora 的普通工具语义并发执行，没有
额外的 Task 并发限制。子运行不向终端 Observer 发送过程事件，只显示父
Agent 的消息以及 Task 完成摘要和耗时。

Task 的上下文隔离不是安全沙箱。父子运行共享进程、当前工作目录、文件
系统、权限、模型客户端和具体工具实例。不需要委派时可以关闭：

```yaml
tools:
  task:
    enabled: false
```

## 图像理解

Dora 将视觉能力视为 model profile 的能力（capability）。能够理解图像的 profile 声明 `capabilities: [text, image_input]`；纯文本模型声明 `capabilities: [text]`。CLI 不提供覆盖或直接附加图片的标志。

当模型想要查看图像时，它会调用始终注册的 `view_image` 工具。该工具接受本地 `path` 或远程 `url`，并以 `image_input` 能力约束临时选择一个视觉模型来识别图片，返回图片的文字描述。图片本身不会进入主模型的上下文——只有返回的描述才会。

Dora 将每个本地图像文件限制为 4 MiB，并拒绝非图像文件。当 `view_image` 调用指向缺失、非图像或过大的文件时，该工具会向模型报告错误，以便其更正路径。如果没有任何 catalog 项声明 `image_input`，`view_image` 调用会报告没有可用的视觉模型。
