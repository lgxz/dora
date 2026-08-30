#!/usr/bin/env bash
#
# run_tb.sh — Harbor Terminal-Bench 评测启动入口（配合 scripts/eval/dora_tb.py 使用）。
#
# 用法：run_tb.sh -m PROVIDER/PROFILE [Harbor 参数...]
# 示例：run_tb.sh -m trust/hy4-preview -n 2
# -m/--model 必填，由本脚本消费，不透传给 Harbor。
#
# 可通过环境变量覆盖的默认值：
#   DORA_BINARY  本地 Linux dora 二进制路径，默认 $SCRIPT_DIR/../../dist/dora-linux-arm64
#   DORA_DATASET Harbor 数据集，默认 terminal-bench@2.1
#   DORA_JOBS_DIR 结果输出目录，默认 $PWD/jobs
#
# 其余 Harbor 参数透传；Agent 和配置入口由本脚本管理，不可另行覆盖。
# 以 dora_tb.yaml 为基础，在临时目录生成本次 Job 配置；命令行模型覆盖
# agents[].model_name，不依赖 Harbor 的 -m 参数合并。可用 --print-config 核对。

set -euo pipefail

usage() {
  echo "用法：$0 -m PROVIDER/PROFILE [Harbor 参数...]"
  echo "示例：$0 -m trust/hy4-preview -n 2"
  echo "-m/--model 必填；模型同时用于 Dora 选模和 Hub 元数据。"
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
    -a|-a?*|--agent|--agent=*|--agent-import-path|--agent-import-path=*|-c|-c?*|--config|--config=*)
      echo "错误：Agent 和 Job 配置由 run_tb.sh 管理，不能传入 $1。" >&2
      exit 1
      ;;
    *)
      harbor_args+=("$1")
      shift
      ;;
  esac
done
if [ -z "$model_spec" ]; then
  usage >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 把 scripts/eval 目录加入 PYTHONPATH，供 harbor 进程 import dora_tb 模块。
export PYTHONPATH="$SCRIPT_DIR${PYTHONPATH:+:$PYTHONPATH}"

# 可覆盖的默认值。
: "${DORA_BINARY:="$SCRIPT_DIR/../../dist/dora-linux-arm64"}"
: "${DORA_DATASET:=terminal-bench/terminal-bench-2-1}"
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

# 与 Dora 一致：provider 名称大写、非字母数字字符替换为下划线。
model_provider="${model_spec%%/*}"
API_KEY_VAR="$(python3 -c 'import sys; print("".join(c.upper() if c.isascii() and c.isalnum() else "_" for c in sys.argv[1]) + "_API_KEY")' "$model_provider")"

if [ -z "${!API_KEY_VAR:-}" ]; then
  echo "错误：缺少环境变量 ${API_KEY_VAR}（当前模型为 ${model_spec}）。" >&2
  echo "请先 export ${API_KEY_VAR}=<your key> 后再运行本脚本。" >&2
  exit 1
fi

# 通过 --ae 注入会传给容器内 dora 的环境变量。至少模型对应 provider 的 key 必须存在。
agent_env_args=(
  "--ae" "${API_KEY_VAR}=${!API_KEY_VAR}"
)

# Harbor 负责解析基础 YAML；Python 标准库只处理 JSON，不额外依赖 PyYAML。
# 临时目录仅当前用户可访问，退出（包括失败和中断）时清理。
job_config_dir="$(mktemp -d "${TMPDIR:-/tmp}/dora-tb.XXXXXX")"
job_config_path="$job_config_dir/job.json"
cleanup_job_config() {
  rm -f "$job_config_path"
  rmdir "$job_config_dir"
}
trap cleanup_job_config EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
(
  umask 077
  harbor run --config "$SCRIPT_DIR/dora_tb.yaml" --print-config > "$job_config_path"
)
python3 - "$job_config_path" "$model_spec" <<'PY'
import json
import sys

path, model = sys.argv[1:]
with open(path, encoding="utf-8") as source:
    config = json.load(source)
agents = config.get("agents", [])
if len(agents) != 1 or agents[0].get("import_path") != "dora_tb:DoraAgent":
    raise SystemExit("Expected exactly one dora_tb:DoraAgent in the base job config")
agents[0]["name"] = "aipymini"
agents[0]["model_name"] = model
with open(path, "w", encoding="utf-8") as output:
    json.dump(config, output, ensure_ascii=False, indent=2)
    output.write("\n")
PY

# 不打印 --ae 或额外参数中的潜在密钥。
echo "> harbor run --config \"$job_config_path\" (agent=aipymini, model=$model_spec)" >&2

# 执行 Harbor Terminal-Bench 评估。
# shellcheck disable=SC2086
harbor run \
  --config "$job_config_path" \
  -d "$DORA_DATASET" \
  "${agent_env_args[@]}" \
  -o "$DORA_JOBS_DIR" \
  ${harbor_args[@]+"${harbor_args[@]}"}
