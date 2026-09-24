#!/usr/bin/env bash
#
# run_tb.sh — Harbor Terminal-Bench 评测启动入口（配合 scripts/eval/aipymini.py 使用）。
#
# 用法：run_tb.sh [-m PROVIDER/PROFILE] [Harbor 参数...]
# 示例：run_tb.sh -m trust/hy4-preview -n 2
# -m/--model 可选，默认 deepseek/deepseek-v4-pro；由本脚本消费，不透传给 Harbor。
#
# 可通过环境变量覆盖的默认值：
#   AIPYMINI_BINARY  本地 Linux 二进制路径，默认 $HOME/.local/bin/dora
#   AIPYMINI_DATASET Harbor 数据集，默认固定为 Terminal-Bench 4.0.0
#   AIPYMINI_JOBS_DIR 结果输出目录，默认 $HOME/jobs
# 同时设置 TELEGRAM_TOKEN 和 TELEGRAM_CHAT_ID 时，运行结束后自动发送通知。
#
# 其余 Harbor 参数透传；Agent、数据集和配置入口由本脚本管理，不可另行覆盖。
# 临时 YAML 包含固定数据集、Agent 名称、加载路径和模型，不需要静态配置文件，
# 也不依赖 Harbor 的 -m/-d 参数合并。其他设置通过 -n 等 Harbor 参数传入。

set -euo pipefail

DEFAULT_MODEL="deepseek/deepseek-v4-pro"

usage() {
  echo "用法：$0 [-m PROVIDER/PROFILE] [Harbor 参数...]"
  echo "示例：$0 -m trust/hy4-preview -n 2"
  echo "-m/--model 可选，默认 ${DEFAULT_MODEL}；模型同时用于 aipymini 选模和 Hub 元数据。"
}

model_spec=""
harbor_args=()
set_model() {
  if [ -n "$model_spec" ]; then
    echo "错误：每次评测只能指定一个 -m/--model。" >&2
    exit 1
  fi
  if [[ ! "$1" =~ ^[^/[:space:]-][^/[:space:]]*/[^/[:space:]]+$ ]]; then
    echo "错误：-m/--model 必须使用非空的 PROVIDER/PROFILE 格式。" >&2
    exit 1
  fi
  model_spec="$1"
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    -m|--model)
      if [ "$#" -lt 2 ]; then
        echo "错误：$1 缺少 PROVIDER/PROFILE。" >&2
        exit 1
      fi
      set_model "$2"
      shift 2
      ;;
    --model=*)
      set_model "${1#*=}"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    -a|-a?*|--agent|--agent=*|--agent-import-path|--agent-import-path=*|-c|-c?*|--config|--config=*|-d|-d?*|--dataset|--dataset=*)
      echo "错误：Agent、数据集和 Job 配置由 run_tb.sh 管理，不能传入 $1。" >&2
      exit 1
      ;;
    *)
      harbor_args+=("$1")
      shift
      ;;
  esac
done
if [ -z "$model_spec" ]; then
  model_spec="$DEFAULT_MODEL"
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 可覆盖的默认值。
: "${AIPYMINI_BINARY:=${HOME}/.local/bin/dora}"
: "${AIPYMINI_DATASET:=terminal-bench/terminal-bench@4.0.0}"
: "${AIPYMINI_JOBS_DIR:="$HOME/jobs"}"
if [[ ! "$AIPYMINI_DATASET" =~ ^[^/@[:space:]]+/[^/@[:space:]]+@[^@[:space:]]+$ ]]; then
  echo "错误：AIPYMINI_DATASET 必须使用 ORG/NAME@REF 格式。" >&2
  exit 1
fi
dataset_name="${AIPYMINI_DATASET%@*}"
dataset_ref="${AIPYMINI_DATASET#*@}"

# 确认本地 Linux 构建产物存在且可执行。
if [ ! -x "$AIPYMINI_BINARY" ]; then
  echo "错误：AIPYMINI_BINARY 不是可执行文件或不存在：$AIPYMINI_BINARY" >&2
  echo "请先用 make release-linux GOARCH=<任务镜像架构> CGO_ENABLED=0 构建静态 Linux 产物，或设置 AIPYMINI_BINARY 覆盖路径。" >&2
  exit 1
fi

# 确认 harbor 已安装。
if ! command -v harbor >/dev/null 2>&1; then
  echo "错误：未找到 harbor 命令。请先安装 harbor（如 pip install harbor）。" >&2
  exit 1
fi

# 与 aipymini 一致：provider 名称大写、非字母数字字符替换为下划线。
model_provider="${model_spec%%/*}"
API_KEY_VAR="$(python3 -c 'import sys; print("".join(c.upper() if c.isascii() and c.isalnum() else "_" for c in sys.argv[1]) + "_API_KEY")' "$model_provider")"

if [ -z "${!API_KEY_VAR:-}" ]; then
  echo "错误：缺少环境变量 ${API_KEY_VAR}（当前模型为 ${model_spec}）。" >&2
  echo "请先 export ${API_KEY_VAR}=<your key> 后再运行本脚本。" >&2
  exit 1
fi

# 通过 --ae 注入会传给容器内 aipymini 的环境变量。至少模型对应 provider 的 key 必须存在。
agent_env_args=(
  "--ae" "${API_KEY_VAR}=${!API_KEY_VAR}"
)

# 临时目录仅当前用户可访问，退出（包括失败和中断）时清理。
run_started_seconds=$SECONDS
job_config_dir=""
job_config_path=""
aipymini_adapter_path=""
aipymini_binary_path=""
send_telegram_notification() {
  local exit_code="$1"
  local elapsed_seconds="$2"
  local status_text host_name message

  if [ -z "${TELEGRAM_TOKEN:-}" ] || [ -z "${TELEGRAM_CHAT_ID:-}" ]; then
    return 0
  fi
  if ! command -v curl >/dev/null 2>&1; then
    echo "警告：已配置 Telegram 通知，但未找到 curl，无法发送通知。" >&2
    return 0
  fi

  case "$exit_code" in
    0) status_text="✅ 完成" ;;
    129|130|143) status_text="⚠️ 中断" ;;
    *) status_text="❌ 失败" ;;
  esac
  host_name="$(hostname 2>/dev/null)"
  if [ -z "$host_name" ]; then
    host_name="unknown"
  fi
  message="$(printf \
    'Terminal-Bench %s\nHost: %s\nAgent: aipymini\nModel: %s\nDataset: %s\nDuration: %02d:%02d:%02d\nExit code: %d' \
    "$status_text" \
    "$host_name" \
    "$model_spec" \
    "$AIPYMINI_DATASET" \
    "$((elapsed_seconds / 3600))" \
    "$(((elapsed_seconds % 3600) / 60))" \
    "$((elapsed_seconds % 60))" \
    "$exit_code"
  )"

  if ! curl -sS --fail \
    --connect-timeout 10 \
    --max-time 30 \
    -X POST \
    "https://api.telegram.org/bot${TELEGRAM_TOKEN}/sendMessage" \
    --data-urlencode "chat_id=${TELEGRAM_CHAT_ID}" \
    --data-urlencode "text=${message}" \
    >/dev/null 2>&1; then
    echo "警告：Telegram 通知发送失败。" >&2
  fi
}
cleanup_job_config() {
  if [ -z "$job_config_dir" ]; then
    return 0
  fi
  rm -f "$job_config_path" "$aipymini_adapter_path" "$aipymini_binary_path"
  rmdir "$job_config_dir"
}
finish_run() {
  local exit_code=$?
  local elapsed_seconds=$((SECONDS - run_started_seconds))
  trap - EXIT
  set +e
  cleanup_job_config
  send_telegram_notification "$exit_code" "$elapsed_seconds"
  exit "$exit_code"
}
trap finish_run EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

job_config_dir="$(mktemp -d "${TMPDIR:-/tmp}/aipymini-tb.XXXXXX")"
job_config_path="$job_config_dir/job.yaml"
aipymini_adapter_path="$job_config_dir/aipymini.py"
aipymini_binary_path="$job_config_dir/aipymini"

# Harbor 会把命令和异常 traceback 写入结果。使用中性临时路径，避免本地
# 仓库名、适配器来源路径和构建产物文件名泄漏到上传结果。
cp "$SCRIPT_DIR/aipymini.py" "$aipymini_adapter_path"
cp "$AIPYMINI_BINARY" "$aipymini_binary_path"
chmod 700 "$aipymini_binary_path"
export AIPYMINI_BINARY="$aipymini_binary_path"
export PYTHONPATH="$job_config_dir${PYTHONPATH:+:$PYTHONPATH}"
export PYTHONDONTWRITEBYTECODE=1

# YAML 单引号字符串通过双写单引号转义；模型和数据集已禁止空白和换行。
yaml_model="$(printf '%s' "$model_spec" | sed "s/'/''/g")"
yaml_dataset_name="$(printf '%s' "$dataset_name" | sed "s/'/''/g")"
yaml_dataset_ref="$(printf '%s' "$dataset_ref" | sed "s/'/''/g")"
(
  umask 077
  printf '%s\n' \
    'datasets:' \
    "  - name: '$yaml_dataset_name'" \
    "    ref: '$yaml_dataset_ref'" \
    '    exclude_task_names:' \
    '      - fp8-rmsnorm-gemm' \
    '      - jax-speedrun-gpu' \
    '      - math-eval-grader' \
    'agents:' \
    '  - name: aipymini' \
    '    import_path: aipymini:AIPyMiniAgent' \
    "    model_name: '$yaml_model'" > "$job_config_path"
)

# 不打印 --ae 或额外参数中的潜在密钥。
echo "> harbor run --config \"$job_config_path\" (dataset=$AIPYMINI_DATASET, agent=aipymini, model=$model_spec)" >&2

# 执行 Harbor Terminal-Bench 评估。
# shellcheck disable=SC2086
harbor run \
  --config "$job_config_path" \
  "${agent_env_args[@]}" \
  -o "$AIPYMINI_JOBS_DIR" \
  ${harbor_args[@]+"${harbor_args[@]}"}
