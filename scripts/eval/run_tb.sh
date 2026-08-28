#!/usr/bin/env bash
#
# run_tb.sh — Harbor Terminal-Bench 评测启动入口（配合 scripts/eval/dora_tb.py 使用）。
#
# 三项已核实的用户决策（与 dora_tb.py 保持一致）：
#   1. 产物经“上传本地 Linux 构建”而非网络下载 —— 通过 DORA_BINARY 指向本地
#      dist/dora-linux-arm64，由 dora_tb.py 的 install() 上传进容器。
#   2. API key 经环境变量注入 —— 由本脚本按所选 model 的 provider 检查对应 key，
#      再通过 `--ae KEY=VALUE` 注入容器内 dora 进程，绝不硬编码 key。
#   3. 无需 session / DORA_POLICY_* —— dora 在容器内保留进度输出运行
#      （dora -m <model_spec>），输出只写入任务日志，不管理 session 数据库、
#      不注入策略变量。Harbor Job 配置由 dora_tb.yaml 提供，使 Hub 将 Agent
#      显示为 aipymini，同时仍通过 dora_tb:DoraAgent 加载适配器。
#
# 可通过环境变量覆盖的默认值：
#   DORA_BINARY  本地 Linux dora 二进制路径，默认 $SCRIPT_DIR/../../dist/dora-linux-arm64
#   DORA_DATASET Harbor 数据集，默认 terminal-bench@2.1
#   DORA_MODEL   dora 模型 spec（-> CLI_FLAGS 的 -m），默认 deepseek/deepseek-v4-flash
#   DORA_JOBS_DIR 结果输出目录，默认 $PWD/jobs
#
# 其余位置参数 `"$@"` 原样透传给 `harbor run`。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 把 scripts/eval 目录加入 PYTHONPATH，供 harbor 进程 import dora_tb 模块。
export PYTHONPATH="$SCRIPT_DIR${PYTHONPATH:+:$PYTHONPATH}"

# 可覆盖的默认值。
: "${DORA_BINARY:="$SCRIPT_DIR/../../dist/dora-linux-arm64"}"
: "${DORA_DATASET:=terminal-bench/terminal-bench-2-1}"
: "${DORA_MODEL:=deepseek/deepseek-v4-flash}"
: "${DORA_JOBS_DIR:="$PWD/jobs"}"
export DORA_BINARY

# 确认本地 Linux 构建产物存在且可执行。
if [ ! -x "$DORA_BINARY" ]; then
  echo "错误：DORA_BINARY 不是可执行文件或不存在：$DORA_BINARY" >&2
  echo "请先用 make release-linux GOARCH=<任务镜像架构> CGO_ENABLED=0 构建静态 Linux 产物，或设置 DORA_BINARY 覆盖路径。" >&2
  exit 1
fi

# 确认 harbor 已安装。
if ! command -v harbor >/dev/null 2>&1; then
  echo "错误：未找到 harbor 命令。请先安装 harbor（如 pip install harbor）。" >&2
  exit 1
fi

# 按所选 model 的 provider 决定检查哪个 API key。
case "$DORA_MODEL" in
  trust/*)      API_KEY_VAR="TRUST_API_KEY" ;;      # trust provider
  deepseek/*)   API_KEY_VAR="DEEPSEEK_API_KEY" ;;   # deepseek provider
  openrouter/*) API_KEY_VAR="OPENROUTER_API_KEY" ;; # openrouter provider
  *)            API_KEY_VAR="TRUST_API_KEY" ;;      # 保持兼容：其它默认走 trust
esac

if [ -z "${!API_KEY_VAR:-}" ]; then
  echo "错误：缺少环境变量 ${API_KEY_VAR}（当前 DORA_MODEL=${DORA_MODEL} 对应该 provider）。" >&2
  echo "请先 export ${API_KEY_VAR}=<your key> 后再运行本脚本。" >&2
  exit 1
fi

# 通过 --ae 注入会传给容器内 dora 的环境变量。至少模型对应 provider 的 key 必须存在。
agent_env_args=(
  "--ae" "${API_KEY_VAR}=${!API_KEY_VAR}"
)
# 若 host 上同时设置了其它内建 provider 的 key，也一并注入（不影响已解析的关键 key）。
for optional_key_var in TRUST_API_KEY DEEPSEEK_API_KEY OPENROUTER_API_KEY; do
  if [ "$optional_key_var" != "$API_KEY_VAR" ] && [ -n "${!optional_key_var:-}" ]; then
    agent_env_args+=("--ae" "${optional_key_var}=${!optional_key_var}")
  fi
done

# 打印即将执行的完整 harbor run 命令，便于核对。
echo "> harbor run --config \"$SCRIPT_DIR/dora_tb.yaml\" -d \"$DORA_DATASET\" -m \"$DORA_MODEL\" --ak model=\"$DORA_MODEL\" ${agent_env_args[*]} -o \"$DORA_JOBS_DIR\" $*"

# 执行 Harbor Terminal-Bench 评估。
# shellcheck disable=SC2086
harbor run \
  --config "$SCRIPT_DIR/dora_tb.yaml" \
  -d "$DORA_DATASET" \
  -m "$DORA_MODEL" \
  --ak model="$DORA_MODEL" \
  "${agent_env_args[@]}" \
  -o "$DORA_JOBS_DIR" \
  "$@"
