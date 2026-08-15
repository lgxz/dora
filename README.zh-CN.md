# dora

[![English](https://img.shields.io/badge/Language-English-blue.svg)](README.md)
[![简体中文](https://img.shields.io/badge/语言-简体中文-red.svg)](README.zh-CN.md)

`dora` 是一个小巧、模块化的 Go 语言 LLM Agent 内核。其核心是一个循环和两个接口：`Model` 和 `Tool`。

无需配置文件：只需设置单个提供商的 API 密钥环境变量（OpenAI、DeepSeek 或 Trustoken），dora 便会自动选择该提供商并立即运行。

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
cat AGENTS.md | dora Summarize the following content | mdcat
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

Dora 从专用的环境变量读取每个提供商的 API 密钥。下表列出了支持的提供商、它们的环境变量和默认模型：

| 提供商 | 环境变量 | 默认模型 |
| --- | --- | --- |
| openai | `OPENAI_API_KEY` | `gpt-5` |
| deepseek | `DEEPSEEK_API_KEY` | `deepseek-v4-flash` |
| trust | `TRUST_API_KEY` | `auto` |

为你要使用的提供商设置环境变量。命令因操作系统而异。

macOS / Linux，临时（仅当前终端）：

```sh
export OPENAI_API_KEY="sk-..."
```

macOS / Linux，永久：将上面的 `export` 行追加到 `~/.zshrc`（zsh）或 `~/.bashrc`（bash），然后重新加载：

```sh
source ~/.zshrc
```

Windows PowerShell，临时（仅当前会话）：

```powershell
$env:OPENAI_API_KEY = "sk-..."
```

Windows PowerShell，永久（对当前用户持久生效）：

```powershell
[Environment]::SetEnvironmentVariable("OPENAI_API_KEY", "sk-...", "User")
```

Windows CMD，临时（仅当前会话）：

```cmd
set OPENAI_API_KEY=sk-...
```

当你为多个提供商都设置了密钥时，请在 `~/.dora/config.yaml` 中显式指定 `model.provider`；否则 Dora 会报告歧义错误。只设置一个密钥可以让 Dora 自动选择该提供商。

当只设置了一个受支持提供商的 API 密钥时，Dora 无需配置文件即可运行，并自动选择该提供商。例如，`DEEPSEEK_API_KEY` 选择 `deepseek`，`OPENAI_API_KEY` 选择 `openai`，`TRUST_API_KEY` 选择 `trust`。如果设置了多个提供商密钥，请显式配置 `model.provider`。如果都没有设置，Dora 保留 `deepseek` 作为回退，并报告缺少 `DEEPSEEK_API_KEY`。

要自定义默认值，请创建 `~/.dora/config.yaml`。Dora 在每个操作系统（包括 macOS）上都使用这个 `~/.dora/` 布局。设置 `DORA_HOME` 环境变量为绝对路径可覆盖 home 目录。你也可以把文件放在任何位置并通过 `--config path/to/config.yaml` 指定；显式请求的文件必须存在。

```yaml
model:
  provider: deepseek
```

显式提供商始终优先于基于环境的选择。当配置文件存在但省略了 `model.provider` 时，同样适用自动选择逻辑。

`deepseek` 预设默认为 `chat_completions` API、`deepseek-v4-flash`、`https://api.deepseek.com` 和 `DEEPSEEK_API_KEY`。`openai` 预设默认为 `chat_completions`、`gpt-5`、`https://api.openai.com/v1` 和 `OPENAI_API_KEY`。`trust` 预设默认为 `chat_completions`、`auto`、`https://api.trustoken.cn/v1` 和 `TRUST_API_KEY`。需要时可覆盖任何预设字段，并将 `api: responses` 设为使用 Responses API。两种 API 都始终使用 SSE 流式输出。Responses 工具循环在本地重放类型化输出项，不依赖服务端的响应存储。

### 第三方 OpenAI 兼容提供商

要使用任何支持 OpenAI Chat Completions 协议的第三方提供商（例如 Ollama、LM Studio、vLLM、Groq、Together、OpenRouter 或自托管端点），请保留 `model.provider: openai` 并覆盖 `base_url`、`name` 和 `api_key_env`（或 `api_key`）。Chat Completions 端点为 `base_url + "/chat/completions"`，因此 `base_url` 应为提供商的 `/v1`（或等效）根路径。

对于无需认证的自托管 Ollama 端点，设置 `api_key_env: ""` 以禁用 API 密钥：

```yaml
model:
  provider: openai
  name: llama3.1
  base_url: http://localhost:11434/v1
  api_key_env: ""
```

对于需要密钥的托管式 OpenAI 兼容服务（如 OpenRouter 或 Groq），将 `api_key_env` 指向自定义环境变量：

```yaml
model:
  provider: openai
  name: openrouter/auto
  base_url: https://openrouter.ai/api/v1
  api_key_env: OPENROUTER_API_KEY
```

对于一次性调用，可在命令行上覆盖模型和基础 URL：

```sh
./dora --model llama3.1 --base-url http://localhost:11434/v1 "prompt"
```

也支持字面量 `api_key`，但使用环境变量可以让密钥不落入配置文件中。非空字面量密钥优先于 `api_key_env`。对于无需认证的本地端点，请显式设置 `api_key_env: ""`。

使用 `max_tokens` 和 `temperature` 控制每个响应的输出预算和采样：

```yaml
model:
  provider: openai
  name: openrouter/auto
  base_url: https://openrouter.ai/api/v1
  api_key_env: OPENROUTER_API_KEY
  max_tokens: 32768
  temperature: 0.7
```

`max_tokens` 限制模型在一次响应中生成的 token 数量，默认为 32768。它在线上以 `max_tokens` 形式发送给 `chat_completions` API，以 `max_output_tokens` 形式发送给 `responses` API；显式设置为 `0` 表示"无显式上限"并按原样传递。`temperature` 没有默认值：省略时不会发送任何值，提供商使用自己的默认采样。它接受 `[0, 2]` 范围内的值。由于某些推理模型和工具调用模型会忽略或拒绝非默认温度，请将 `temperature` 视为尽力而为。这两个键仅在 `model.` 中设置时才生效；它们没有对应的命令行标志。

`thinking` 控制模型的"思考模式"推理强度。将其设置为 `off`、`minimal`、`low`、`medium` 或 `high` 之一。它没有默认值：省略时不会发送任何值，提供商使用自己的推理默认值。`deepseek` 预设是例外，默认为 `off`；请显式设置 `thinking` 来覆盖。
不同提供商的支持情况各异，不支持的值会被静默忽略而不是报错：

- **openai**：在 Responses API 上会发送 `off`→`none`、`minimal`、`low`、`medium` 和 `high` 全部值；在 Chat Completions 上 `minimal`–`high` 会以 `reasoning_effort` 发送，但 `off` 不受支持（gpt-5 的下限是 `minimal`），并会被忽略。
- **deepseek**：在两个 API 上都会发送 `low`/`medium`/`high`，`minimal` 不受支持并被忽略，而 `off` 在 Chat Completions 上以 `thinking.type: disabled` 发送，在 Responses 上以 `reasoning.effort: none` 发送。
- **trust**：在两个 API 上都按尽力而为的方式处理，类似 OpenAI。

由于一个设置可能被直接丢弃，请将 `thinking` 视为提示而非保证。对于一次性调用，`--thinking` 会用 `off`、`minimal`、`low`、`medium` 或 `high` 之一覆盖配置的 `model.thinking`：

```sh
./dora --thinking high "Solve a hard problem"
```

Dora 默认每个片段最多运行 256 轮模型-工具循环。请保留这一安全机制，但在工具工作流异常冗长时进行调整：

```yaml
agent:
  max_rounds: 96
```

使用 `--max-rounds` 为一次调用覆盖它：

```sh
./dora --max-rounds 96 "Complete a long task"
```

默认情况下，Dora 每次迭代会向模型发送最近 32 轮，并压缩更早的历史，避免长时间的工具循环无界地增长上下文。使用 `--max-history-rounds` 为一次调用覆盖保留的轮数（使用 `0` 可禁用压缩并发送全部历史）：

```sh
./dora --max-history-rounds 64 "Complete a long task"
```

当 stdin 和 stderr 都连接到终端且达到限制时，Dora 会询问是否继续到下一个片段。确认后将从已完成的工具输出处恢复，而不重放已完成的工作。拒绝则正常停止并保存命名会话的部分状态。使用管道或重定向 I/O 时，Dora 不会提示，而是返回 `dora: maximum rounds exceeded`。

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

颜色会自动在终端输出中启用。设置 `NO_COLOR=1` 以在无 ANSI 颜色的情况下保留布局；进度仍显示在 stderr 上。

### 会话

使用会话名称可以在多次 CLI 调用之间继续同一个对话：

```sh
./dora -s system-status "Analyze this machine's system status"
./dora -s system-status "Continue with the busiest processes"
```

使用 `--fresh` 在同一个会话名称下重新开始。本次运行会忽略已有历史，并且仅在新任务成功后替换历史；如果运行失败，之前的会话保持不变：

```sh
./dora -s system-status --fresh "Analyze this machine from scratch"
```

会话名称可包含字母、数字、`.`、`_` 和 `-`。Dora 将每个会话存储为带版本的 JSON 快照，权限为 `0600`。会话 v3 绑定了配置的提供商、API、模型和基础 URL：Chat Completions 从消息恢复，而 Responses 额外持久化其不透明的类型化项续接。在更改会话的后端之前使用 `--fresh`。不支持版本 1 和版本 2 的会话文件。默认目录在每个操作系统上都是 `~/.dora/sessions`。省略 `--session`/`-s` 可保持现有的无状态行为。会话文件可能包含命令和工具输出，因此请将其视为敏感内容。请勿同时对同一个会话名称运行两个 Dora 进程。

使用 `--config`、`--model`、`--base-url`、`--thinking`、`--max-rounds`、`--max-history-rounds`、`--skills-dir` 或 `--no-skills` 可为一次调用覆盖相应的配置。

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

默认情况下，Dora 会在 `~/.dora/skills`（或 `DORA_HOME/skills`）发现 `skills` 目录，与活动的 `config.yaml` 路径无关。无需任何配置。

仅当需要添加更多父目录时使用 `skills.directories`：

```yaml
skills:
  directories:
    - /absolute/path/to/additional-skills
```

对于一次性运行，可在命令行添加一个或多个父目录：

```sh
dora --skills-dir ./project-skills --skills-dir ~/shared-skills "Run checks"
```

命令行目录会与默认目录和配置目录合并，转换为绝对路径并去重。使用 `--no-skills` 可为一次调用禁用所有技能来源；它优先于 `--skills-dir` 和 `skills.directories` 两者。

Dora 仅在 `skill` 工具 schema 中公布技能名称和描述。当模型调用该工具时，会返回技能的绝对目录和完整的 `SKILL.md`，从而允许指令引用诸如 `scripts/check.sh` 之类的文件。技能工具从不执行这些文件；执行仍然需要一个已启用的工具（如 Bash）。名称必须包含小写字母、数字和连字符，并且必须与其目录名匹配。会拒绝重复的名称。缺失或为空的默认目录只会让该工具保持禁用；畸形的已发现技能和缺失的显式配置或命令行目录则属于错误。

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
```

可执行文件不存在的自动工具会被跳过。用 `enabled: true` 显式启用的工具必须存在于 `PATH` 上，否则 Dora 会报告错误。目前发现机制仅检查可执行文件是否存在；它不会启动 shell 去探测其运行时环境。

Bash 工具在 Dora 的当前目录下运行 `bash -lc`。当模型需要其他目录时，可以在命令内部使用 `cd`。该工具以 JSON 形式向模型返回退出码、stdout、stderr、超时和截断信息。每个流的输出限制为 1 MiB。此工具授予模型与 `dora` 进程相同的文件系统和进程权限，因此除非你信任 Dora 运行的环境，否则请禁用它。

独立的 `powershell` 工具优先使用 PowerShell Core（`pwsh`），并在不可用时回退到 Windows PowerShell（`powershell.exe`）。如果两个工具都被显式启用，它们会分开暴露，以便区分各自的命令语法。

PowerShell 也在 Dora 的当前目录中启动，需要时可在命令内部使用 `Set-Location`。

两个命令工具都接受可选的每次命令超时。它覆盖该次调用的配置默认值，且不能超过 3600 秒：

```json
{
  "command": "go build ./...",
  "timeout_seconds": 300
}
```

省略时，`timeout_seconds` 来自对应的 YAML 工具设置；当该设置为零或缺失时，默认 120 秒。

## 图像理解

Dora 可以向消息附加图像，让多模态（视觉）模型能够"看到"它们。图像理解取决于配置的模型：默认的 DeepSeek 模型可能不支持视觉，因此请显式使用 `--vision` 或在配置中设置 `model.vision: true` 来启用它，并选择支持视觉的模型（例如 OpenAI 的 `gpt-4o` 类模型）。

使用可重复的 `--image` 标志将本地图像附加到当前提示（需要已启用视觉功能）：

```sh
./dora --vision --model gpt-4o --image photo.png "Describe this photo"
```

模型也可以自己展示图像：当命令工具的 stdout 包含 `@@path@@` 标签时，Dora 会解析它并将该路径的图像附加到工具消息中。命令工具的描述文档中记录了这一约定，以便模型知道可以发出这样的标签。

图像会消耗模型上下文：每个附加的图像都会被编码为视觉 token，计入模型的上下文窗口，因此大图或多图可能耗尽小的上下文窗口。Dora 将每个图像文件限制为 4 MiB，并拒绝非图像文件。当 `@@path@@` 标签指向缺失、非图像或过大的文件时，Dora 不会附加它，而是向模型报告错误，以便其更正路径。