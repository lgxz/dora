"""aipymini — Harbor Terminal-Bench agent adapter.

This module adapts **dora** (the terminal LLM agent written in Go, see
`/Users/lgx/Src/dora`) so it can be driven by the Harbor evaluation
framework (`harbor run --dataset terminal-bench@2.1`).

Class
-----
* :class:`AIPyMiniAgent` — a :class:`harbor.agents.installed.base.BaseInstalledAgent`
  subclass that uploads a prebuilt **local Linux** dora binary into the sandbox
  and runs it against each task instruction.

Confirmed design decisions (agreed with the user)
-------------------------------------------------
1. **Binary ships by upload, not download.**
   ``install()`` uploads the locally compiled ``GOOS=linux`` dora binary into
   the sandbox at ``/installed-agent/aipymini`` via ``environment.upload_file``,
   then makes it executable. No network download / no npm / no online install.
2. **API keys are injected through environment variables.**
   dora reads its keys from environment variables (e.g. ``TRUST_API_KEY``,
   ``DEEPSEEK_API_KEY``, or ``OPENROUTER_API_KEY``). These are supplied at
   ``harbor run`` time through ``extra_env`` / ``--ae KEY=VALUE`` /
   ``AgentConfig.env``. The adapter only *declares* them via ``ENV_VARS`` and
   transparently forwards the matching host variables to the sandboxed dora
   process — it never hard-codes a key.
3. **No session / DORA_POLICY_* needed to test.**
   Inside the container dora runs with progress enabled
   (``dora -m <model_spec>``). The adapter sets no ``DORA_POLICY_*``, does
   not manage the session database, and does not ship skills. The model is
   selected from Harbor's ``model_name`` and passed to dora's
   ``--model PROVIDER/PROFILE`` flag. The same value supplies Hub metadata.
   The legacy ``kwargs.model`` / ``--ak model`` entry point is not supported.

How to run
----------
The module must be importable by the Harbor Python process. Either place
``scripts/eval`` on ``PYTHONPATH`` or ``pip install -e .`` the project, then::

    harbor run --dataset terminal-bench@2.1 --agent aipymini:AIPyMiniAgent -m openrouter/auto

For the ``aipymini`` Agent name in Hub, use ``run_tb.sh``. It directly writes a minimal
private temporary YAML config containing the Agent name, import path, and
``model_name``. It defaults to ``deepseek/deepseek-v4-pro`` and can be overridden
with ``-m PROVIDER/PROFILE``. The temporary config is removed on exit. No static
configuration file is needed; pass other job settings through Harbor flags such
as ``-n``. No ``-m`` or ``--ak model`` is passed to Harbor.
For example: ``scripts/eval/run_tb.sh -m trust/hy4-preview -n 2``.
If both ``TELEGRAM_TOKEN`` and ``TELEGRAM_CHAT_ID`` are set, the wrapper sends a
completion, failure, or interruption notification through the Telegram Bot API.

The local Linux binary path is given via the host ``AIPYMINI_BINARY``
environment variable (or the constructor kwarg ``aipymini_binary``). API keys go
through ``extra_env`` (``--ae``)::

    --ak aipymini_binary=/path/to/aipymini-linux \\
    --ae OPENROUTER_API_KEY=$OPENROUTER_API_KEY

Everything below intentionally only imports the API reference types inside
``try/except`` so that this file still passes ``python3 -m py_compile`` on a
machine without harbor installed (a readable error is raised only when the
class is actually used).
"""

from __future__ import annotations

import json
import logging
import os
import shlex
import uuid
from pathlib import Path
from typing import Any, override

# Importing harbor is only required at runtime (when the class is used by the
# Harbor process). We keep the imports in a try/except so that basic syntax
# validation (``py_compile``) works on machines where harbor is not installed.
try:  # pragma: no cover - exercised when harbor is available
    from harbor.agents.installed.base import BaseInstalledAgent, CliFlag, EnvVar
    from harbor.environments.base import BaseEnvironment
    from harbor.models.agent.context import AgentContext
    from harbor.models.trajectories import (
        Agent,
        FinalMetrics,
        Metrics,
        Observation,
        ObservationResult,
        Step,
        ToolCall,
        Trajectory,
    )
    from harbor.utils.trajectory_utils import format_trajectory_json
except ImportError as _imp_err:  # pragma: no cover
    _BaseInstalledAgentBase = object
    CliFlag = None  # type: ignore[assignment,misc]
    EnvVar = None  # type: ignore[assignment,misc]
    BaseEnvironment = None  # type: ignore[assignment,misc]
    AgentContext = None  # type: ignore[assignment,misc]
    _HARBOR_IMPORT_ERROR = _imp_err
else:  # pragma: no cover
    _BaseInstalledAgentBase = BaseInstalledAgent
    _HARBOR_IMPORT_ERROR = None

logger = logging.getLogger(__name__)

BINARY_PATH = "/installed-agent/aipymini"
TRACE_PATH = "/logs/agent/trace.json"
TRAJECTORY_PATH = "/logs/agent/trajectory.json"


def _optional_token_count(metrics: dict[str, Any], key: str) -> int | None:
    value = metrics.get(key)
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise ValueError(f"{key} must be a non-negative integer or null")
    return value


def _atif_metrics(usage: Any) -> Metrics | None:
    if usage is None:
        return None
    if not isinstance(usage, dict):
        raise ValueError("usage must be an object or null")
    input_details = usage.get("input_details")
    output_details = usage.get("output_details")
    if input_details is not None and not isinstance(input_details, dict):
        raise ValueError("input_details must be an object or null")
    if output_details is not None and not isinstance(output_details, dict):
        raise ValueError("output_details must be an object or null")
    extra: dict[str, int] = {}
    total_tokens = _optional_token_count(usage, "total_tokens")
    if total_tokens is not None:
        extra["total_tokens"] = total_tokens
    if output_details is not None:
        reasoning_tokens = _optional_token_count(output_details, "reasoning_tokens")
        if reasoning_tokens is not None:
            extra["reasoning_tokens"] = reasoning_tokens
    return Metrics(
        prompt_tokens=_optional_token_count(usage, "input_tokens"),
        completion_tokens=_optional_token_count(usage, "output_tokens"),
        cached_tokens=(
            _optional_token_count(input_details, "cached_tokens")
            if input_details is not None
            else None
        ),
        extra=extra or None,
    )


class AIPyMiniAgent(BaseInstalledAgent):  # type: ignore[misc,valid-type]
    """Harbor :class:`BaseInstalledAgent` adapter for the Go ``dora`` CLI agent.

    The adapter uploads a prebuilt local Linux dora binary into the sandbox
    (``/installed-agent/aipymini``) and runs it against the task instruction that
    is piped to stdin as a shell command, mirroring the ``claude_code``
    adapter's execution pattern.
    """

    # Whether config files are supported — keep the library default (False).
    SUPPORTS_CONFIG: bool = False

    # Model selection comes exclusively from Harbor's model_name.
    # Only presentation flags are configured through agent kwargs.
    CLI_FLAGS: list[CliFlag] = [
        CliFlag(
            kwarg="quiet",
            cli="--quiet",
            type="bool",
            default=False,
        ),
    ]

    # Environment variables that dora reads inside the sandbox and that Harbor
    # should expose as configurable options (injected via extra_env / --ae).
    # kwarg name and env name are kept identical so that
    # ``AgentConfig.env`` / ``harbor run --ae KEY=VALUE`` maps directly.
    ENV_VARS: list[EnvVar] = [
        EnvVar(kwarg="TRUST_API_KEY", env="TRUST_API_KEY", type="str", default=None),
        EnvVar(kwarg="DEEPSEEK_API_KEY", env="DEEPSEEK_API_KEY", type="str", default=None),
        EnvVar(kwarg="OPENROUTER_API_KEY", env="OPENROUTER_API_KEY", type="str", default=None),
    ]

    SYSTEM_DEPENDENCIES: tuple[str, ...] = (
        "curl",
        "python3",
    )

    CA_BUNDLE_PATHS: tuple[str, ...] = (
        "/etc/ssl/certs/ca-certificates.crt",
        "/etc/pki/tls/certs/ca-bundle.crt",
        "/etc/ssl/cert.pem",
    )

    def __init__(self, *args: Any, **kwargs: Any) -> None:
        if "model" in kwargs:
            raise ValueError(
                "kwargs.model / --ak model is not supported; use Harbor's "
                "model_name (or -m) instead."
            )
        super().__init__(*args, **kwargs)
        if not self.model_name or not self.model_name.strip():
            raise ValueError(
                "model_name is required; set agents[].model_name in the Harbor "
                "job config or pass Harbor -m."
            )

    @override
    def build_cli_flags(self) -> str:
        model_flag = shlex.join(["--model", self.model_name])
        other_flags = super().build_cli_flags()
        return f"{model_flag} {other_flags}" if other_flags else model_flag

    @staticmethod
    @override
    def name() -> str:
        """Static agent name recorded in Harbor's result metadata."""
        return "aipymini"

    @override
    def get_version_command(self) -> str | None:
        return f"{BINARY_PATH} --version"

    @override
    def parse_version(self, stdout: str) -> str:
        """Return the version, commit, and build date emitted by the CLI."""
        text = stdout.strip()
        return text or "unknown"

    # -- configurable binaries -------------------------------------------------

    def _resolve_local_binary(self) -> Path:
        """Resolve the local Linux aipymini binary path to upload.

        Order of precedence:
          1. ``aipymini_binary`` kwarg passed to the constructor.
          2. the ``AIPYMINI_BINARY`` environment variable.

        Raises a clear error when neither is provided.
        """
        path_str: str | None = getattr(self, "_flag_kwargs", {}).get("aipymini_binary") or os.environ.get("AIPYMINI_BINARY")
        if not path_str:
            raise RuntimeError(
                "No aipymini binary configured. Provide the local Linux build via "
                "``AIPYMINI_BINARY`` or the ``aipymini_binary`` kwarg."
            )
        local = Path(path_str).expanduser()
        if not local.is_file():
            raise RuntimeError(f"AIPYMINI_BINARY does not point to a file: {local}")
        return local

    # -- installation ----------------------------------------------------------

    async def _install_optional_tooling(self, environment: BaseEnvironment) -> None:
        """Best-effort installation of common task tools.

        Debian packages are installed only when their corresponding command or
        CA bundle is absent. APT state is refreshed from scratch on every retry
        so a stale CDN index cannot make agent setup fail permanently.
        """
        apt_check = await environment.exec(
            command="command -v apt-get >/dev/null 2>&1",
            user="root",
        )
        if apt_check.return_code != 0:
            await self.ensure_system_dependencies(
                environment, self.SYSTEM_DEPENDENCIES
            )
            ca_check = " || ".join(
                f"test -s {shlex.quote(path)}" for path in self.CA_BUNDLE_PATHS
            )
            ca_result = await environment.exec(command=ca_check, user="root")
            if ca_result.return_code != 0:
                await self.ensure_system_dependencies(
                    environment, ("ca_certificates",)
                )
            return

        checks = (
            ("curl", "command -v curl >/dev/null 2>&1"),
            ("python3", "command -v python3 >/dev/null 2>&1"),
            (
                "ca-certificates",
                " || ".join(
                    f"test -s {shlex.quote(path)}"
                    for path in self.CA_BUNDLE_PATHS
                ),
            ),
            ("file", "command -v file >/dev/null 2>&1"),
            ("python3-pip", "python3 -m pip --version >/dev/null 2>&1"),
        )
        missing: list[str] = []
        for package, check in checks:
            result = await environment.exec(command=check, user="root")
            if result.return_code != 0:
                missing.append(package)
        if not missing:
            return

        packages = shlex.join(missing)
        await self.exec_as_root(
            environment,
            command=(
                "attempt=1; while [ \"$attempt\" -le 3 ]; do "
                "rm -rf /var/lib/apt/lists/*; "
                "if apt-get update -qq -o Acquire::Retries=3 && "
                "DEBIAN_FRONTEND=noninteractive apt-get install -y "
                f"--no-install-recommends {packages}; then exit 0; fi; "
                "[ \"$attempt\" -eq 3 ] && exit 1; "
                "attempt=$((attempt + 1)); sleep 3; "
                "done"
            ),
        )

    async def install(self, environment: BaseEnvironment) -> None:  # type: ignore[override]
        """Install the aipymini binary into the sandbox.

        Steps:
          1. Best-effort installation of common task tools.
          2. Resolve and upload the local Linux dora binary to
             ``/installed-agent/aipymini`` and make it executable.
          3. Verify it runs via ``aipymini --version`` and log the output.
        """
        if _HARBOR_IMPORT_ERROR is not None:
            raise RuntimeError(
                "Harbor is not installed/importable in this Python environment: "
                f"{_HARBOR_IMPORT_ERROR}. Run inside the harbor python env."
            ) from _HARBOR_IMPORT_ERROR

        # Tool availability improves task coverage but is not required to
        # upload or launch the static binary. Package-manager and mirror
        # failures therefore degrade to a warning instead of failing a trial.
        try:
            await self._install_optional_tooling(environment)
        except Exception as exc:
            self.logger.warning("optional task tooling installation failed: %s", exc)

        local_binary = self._resolve_local_binary()

        target = BINARY_PATH

        # upload_file copies as root; make it executable (and owned by the agent
        # user when one is configured) so the agent can run it.
        await environment.upload_file(local_binary, target)
        await self.exec_as_root(
            environment,
            command=f"chmod +x {shlex.quote(target)}"
            + (
                f" && chown {shlex.quote(str(environment.default_user))} {shlex.quote(target)}"
                if environment.default_user is not None
                else ""
            ),
        )

        # Verify the uploaded binary is runnable.
        result = await self.exec_as_root(environment, command=f"{target} --version")

    # -- execution -------------------------------------------------------------

    async def run(self, instruction: str, environment: BaseEnvironment, context: AgentContext) -> None:  # type: ignore[override]
        """Run aipymini against one task instruction inside the sandbox.

        The instruction is written into a random shell environment variable and
        piped to aipymini's stdin (following the ``claude_code`` pattern), the
        merged output is redirected to ``/logs/agent/run.txt`` without being
        forwarded to Harbor's console. Passing the
        instruction via stdin (rather than as a command-line positional
        argument) avoids Go's ``flag`` parser treating an instruction that
        starts with ``-`` (e.g. a Markdown list item) as a flag.
        """
        if _HARBOR_IMPORT_ERROR is not None:
            raise RuntimeError(
                "Harbor is not installed/importable in this Python environment: "
                f"{_HARBOR_IMPORT_ERROR}."
            ) from _HARBOR_IMPORT_ERROR

        # Use the same model_name as Harbor's result metadata.
        cli_flags = self.build_cli_flags()
        extra_flags = (cli_flags + " ") if cli_flags else ""

        # Merge declared env vars into the run environment so their values flow
        # through to the sandboxed dora process.
        run_env: dict[str, str] = {}
        for resolved in self.resolve_env_vars().items():
            key, value = resolved
            if value is not None:
                run_env[str(key)] = str(value)
        # self.extra_env (from AgentConfig.env / --ae) is layered onto each exec
        # by the orchestrator via scoped_exec_env; no need to splice it ourselves.

        # Pass the instruction to dora via stdin instead of argv. dora parses
        # argv with Go's flag package, which would reject an instruction that
        # starts with '-' (e.g. a Markdown list item). A random var name avoids
        # leaking/conflicting; unset keeps the value out of the process env.
        instruction_shell_var = "instruction_" + uuid.uuid4().hex
        instruction_env_var = instruction_shell_var.upper()
        run_env[instruction_env_var] = instruction

        command = (
            "export PATH=\"$HOME/.local/bin:$PATH\"; "
            f"{instruction_shell_var}=\"${instruction_env_var}\"; "
            f"unset {instruction_env_var}; "
            "set -o pipefail; "
            f'printf "%s" "${{{instruction_shell_var}}}" | '
            f"{BINARY_PATH} --trace-file {shlex.quote(TRACE_PATH)} "
            f"{extra_flags}> /logs/agent/run.txt 2>&1"
        )

        try:
            await self.exec_as_agent(environment, command=command, env=run_env)
        except Exception as exc:  # NonZeroAgentExitCodeError and friends
            # The full transcript lives in /logs/agent/run.txt for post-hoc
            # inspection regardless of exit status.
            self.logger.warning("aipymini run exited with an error: %s", exc)
            raise

    @override
    def populate_context_post_run(self, context: AgentContext) -> None:
        """Create ATIF output and populate Harbor's aggregate token fields."""
        trajectory = self._write_trajectory()
        if trajectory is None or trajectory.final_metrics is None:
            return

        context.n_input_tokens = trajectory.final_metrics.total_prompt_tokens
        context.n_output_tokens = trajectory.final_metrics.total_completion_tokens
        context.n_cache_tokens = trajectory.final_metrics.total_cached_tokens

    def _write_trajectory(self) -> Trajectory | None:
        trace_path = self.logs_dir / Path(TRACE_PATH).name
        try:
            trace = json.loads(trace_path.read_text(encoding="utf-8"))
            trajectory = self._trajectory_from_trace(trace)
            output = self.logs_dir / Path(TRAJECTORY_PATH).name
            output.write_text(
                format_trajectory_json(trajectory.to_json_dict()),
                encoding="utf-8",
            )
            return trajectory
        except FileNotFoundError:
            self.logger.warning("aipymini did not produce %s", trace_path.name)
        except (OSError, json.JSONDecodeError, KeyError, TypeError, ValueError) as exc:
            self.logger.warning("could not create aipymini ATIF trajectory: %s", exc)
        return None

    def _trajectory_from_trace(self, trace: Any) -> Trajectory:
        if not isinstance(trace, dict):
            raise ValueError("trace must be a JSON object")
        if trace.get("schema_version") != 2:
            raise ValueError("unsupported trace schema_version")

        steps: list[Step] = []

        def append_step(**kwargs: Any) -> None:
            steps.append(Step(step_id=len(steps) + 1, **kwargs))

        system = trace.get("system")
        if system:
            if not isinstance(system, str):
                raise ValueError("system must be a string")
            append_step(source="system", message=system)

        user = trace.get("user")
        if not isinstance(user, str):
            raise ValueError("user must be a string")
        append_step(source="user", message=user)

        usages: list[Metrics] = []
        rounds = trace.get("rounds")
        attempts = trace.get("attempts")
        if not isinstance(rounds, list) or not isinstance(attempts, list):
            raise ValueError("rounds and attempts must be arrays")
        if trace.get("status") not in ("completed", "failed", "incomplete"):
            raise ValueError("invalid trace status")
        # Attempts are the single accounting source. Rounds/final duplicate
        # accepted responses for conversation access and must not be summed.
        for attempt in attempts:
            if not isinstance(attempt, dict):
                raise ValueError("attempt must be an object")
            metrics = _atif_metrics(attempt.get("usage"))
            if metrics is not None:
                usages.append(metrics)
            content = attempt.get("content", "")
            reasoning = attempt.get("reasoning")
            if not isinstance(content, str) or (reasoning is not None and not isinstance(reasoning, str)):
                raise ValueError("attempt content/reasoning must be strings")
            atif_calls: list[ToolCall] = []
            observations: list[ObservationResult] = []
            if attempt.get("disposition") == "tools":
                index = attempt.get("round_index")
                if not isinstance(index, int) or not 0 <= index < len(rounds):
                    raise ValueError("executed attempt has no complete tool round")
                round_data = rounds[index]
                for call in round_data["assistant"]["tool_calls"]:
                    raw_input = call["input"]
                    if not isinstance(raw_input, str):
                        raise ValueError("tool input must be a string")
                    try:
                        arguments = json.loads(raw_input)
                    except json.JSONDecodeError:
                        arguments = {"_raw": raw_input}
                    if not isinstance(arguments, dict):
                        arguments = {"_raw": arguments}
                    atif_calls.append(ToolCall(tool_call_id=call["id"], function_name=call["name"], arguments=arguments))
                for result in round_data["tools"]:
                    images = result.get("images")
                    observations.append(ObservationResult(
                        source_call_id=result["tool_call_id"], content=result.get("content", ""),
                        extra={"images": images} if images else None,
                    ))
            extra = {key: attempt[key] for key in (
                "purpose", "recovery", "disposition", "finish_reason", "raw_finish_reason", "output_budget", "error"
            ) if key in attempt}
            if attempt.get("tool_calls") and not atif_calls:
                extra["unexecuted_tool_calls"] = attempt["tool_calls"]
            append_step(
                source="agent", message=content, reasoning_content=reasoning or None,
                tool_calls=atif_calls or None,
                observation=Observation(results=observations) if observations else None,
                metrics=metrics, llm_call_count=1, extra=extra or None,
            )

        def sum_metric(name: str) -> int | None:
            values = [getattr(item, name) for item in usages]
            reported = [value for value in values if value is not None]
            return sum(reported) if reported else None

        return Trajectory(
            schema_version="ATIF-v1.7",
            session_id=str(uuid.uuid4()),
            trajectory_id=str(uuid.uuid4()),
            agent=Agent(
                name=self.name(),
                version=self._version or "unknown",
                model_name=self.model_name,
            ),
            steps=steps,
            final_metrics=FinalMetrics(
                total_prompt_tokens=sum_metric("prompt_tokens"),
                total_completion_tokens=sum_metric("completion_tokens"),
                total_cached_tokens=sum_metric("cached_tokens"),
                total_steps=len(steps),
            ),
        )
