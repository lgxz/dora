"""dora — Harbor Terminal-Bench agent adapter.

This module adapts **dora** (the terminal LLM agent written in Go, see
`/Users/lgx/Src/dora`) so it can be driven by the Harbor evaluation
framework (`harbor run --dataset terminal-bench@2.1`).

Class
-----
* :class:`DoraAgent` — a :class:`harbor.agents.installed.base.BaseInstalledAgent`
  subclass that uploads a prebuilt **local Linux** dora binary into the sandbox
  and runs it against each task instruction.

Confirmed design decisions (agreed with the user)
-------------------------------------------------
1. **Binary ships by upload, not download.**
   ``install()`` uploads the locally compiled ``GOOS=linux`` dora binary into
   the sandbox at ``/installed-agent/dora`` via ``environment.upload_file``,
   then makes it executable. No network download / no npm / no online install.
2. **API keys are injected through environment variables.**
   dora reads its keys from environment variables (e.g. ``TRUST_API_KEY``,
   ``DEEPSEEK_API_KEY``). These are supplied at ``harbor run`` time through
   ``extra_env`` / ``--ae KEY=VALUE`` / ``AgentConfig.env``. The adapter only
   *declares* them via ``ENV_VARS`` and transparently forwards the matching
   host variables to the sandboxed dora process — it never hard-codes a key.
3. **No session / DORA_POLICY_* needed to test.**
   Inside the container dora runs with its own defaults
   (``dora -q -m <model_spec>``). The adapter sets no ``DORA_POLICY_*``, does
   not manage the session database, and does not ship skills. The model is
   selected through dora's ``-m PROVIDER/PROFILE`` flag (e.g.
   ``-m trust/deepseek-v4-flash``).

How to run
----------
The module must be importable by the Harbor Python process. Either place
``scripts/eval`` on ``PYTHONPATH`` or ``pip install -e .`` the project, then::

    harbor run --dataset terminal-bench@2.1 --agent dora_tb:DoraAgent

The local Linux dora binary path is given via ``--ae DORA_BINARY=/path/to/dora-linux``
(or as the constructor kwarg ``dora_binary``). Model / API keys go through
``extra_env`` (``--ae``)::

    --ae DORA_BINARY=/path/to/dora-linux \\
    --ae model=trust/deepseek-v4-flash \\
    --ae TRUST_API_KEY=$TRUST_API_KEY

Everything below intentionally only imports the API reference types inside
``try/except`` so that this file still passes ``python3 -m py_compile`` on a
machine without harbor installed (a readable error is raised only when the
class is actually used).
"""

from __future__ import annotations

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

BINARY_PATH = "/installed-agent/dora"

class DoraAgent(BaseInstalledAgent):  # type: ignore[misc,valid-type]
    """Harbor :class:`BaseInstalledAgent` adapter for the Go ``dora`` CLI agent.

    The adapter uploads a prebuilt local Linux dora binary into the sandbox
    (``/installed-agent/dora``) and runs it against the task instruction that
    is piped to stdin as a shell command, mirroring the ``claude_code``
    adapter's execution pattern.
    """

    # Whether config files are supported — keep the library default (False).
    SUPPORTS_CONFIG: bool = False

    # CLI flags forwarded to the dora binary. kwarg ``model`` maps to ``-m``
    # (PROVIDER/PROFILE, e.g. "trust/deepseek-v4-flash"); kwarg ``quiet`` maps
    # to ``-q`` (hide run progress, default True).
    CLI_FLAGS: list[CliFlag] = [
        CliFlag(
            kwarg="model",
            cli="-m",
            type="str",
            default=None,
        ),
        CliFlag(
            kwarg="quiet",
            cli="-q",
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
    ]

    # Minimal set of system commands dora needs inside the container.
    # curl / ca_certificates - HTTP + TLS trust store for model API calls and
    #                         task downloads.
    # python3     - dora's most-used scripting runtime (agents fall back to it
    #               for JSON handling, parsing, and one-off automation).
    # procps      - ``ps``/``kill`` for process inspection; absent in slim
    #               images and used in ~1/5 of Terminal-Bench trials.
    # expect      - interactive automation (serial consoles, installers);
    #               required by the qemu/interactive task family.
    # telnet      - serial-console client for the qemu task family.
    # netcat, socat - port probing / forwarding / PTY bridging.
    # file        - identifying image and binary formats.
    # python3-pip, python3-venv - let agents ``pip install`` missing runtimes
    #               (e.g. torch). venv is required alongside pip: on PEP 668
    #               externally-managed systems (Ubuntu noble+) a system-wide
    #               install is refused, so the standard path is
    #               ``python3 -m venv`` + the venv's pip.
    # Chosen from the jobs/082265 transcripts: what agents used vs. had to
    # install mid-trial (jq/ripgrep/tmux showed no usage — python3 and grep
    # already cover them — and are deliberately omitted).
    SYSTEM_DEPENDENCIES: tuple[str, ...] = (
        "curl",
        "python3",
        "ca_certificates",
        "procps",
        "expect",
        "telnet",
        "netcat",
        "socat",
        "file",
        "python3-pip",
        "python3-venv",
    )

    @staticmethod
    @override
    def name() -> str:
        """Static agent name, used by Harbor via ``import_path``."""
        return "aipymini"

    @override
    def get_version_command(self) -> str | None:
        return f"{BINARY_PATH} --version"

    @override
    def parse_version(self, stdout: str) -> str:
        """Extract the version from dora --version output.

        dora --version prints: 'dora <version> (commit <commit>, built <date>)'.
        We return the full string so the commit/date are preserved in the
        agent_info, but strip the leading 'dora ' prefix.
        """
        text = stdout.strip()
        if text.startswith("dora "):
            return text[len("dora "):]
        return text or "unknown"

    # -- configurable binaries -------------------------------------------------

    def _resolve_local_binary(self) -> Path:
        """Resolve the local Linux dora binary path to upload.

        Order of precedence:
          1. ``dora_binary`` kwarg passed to the constructor.
          2. the ``DORA_BINARY`` environment variable (``--ae DORA_BINARY=...``).

        Raises a clear error when neither is provided.
        """
        path_str: str | None = getattr(self, "_flag_kwargs", {}).get("dora_binary") or os.environ.get("DORA_BINARY")
        if not path_str:
            raise RuntimeError(
                "No dora binary configured. Provide the local Linux build via "
                "``DORA_BINARY`` (e.g. ``--ae DORA_BINARY=/path/to/dora-linux``) "
                "or the ``dora_binary`` kwarg."
            )
        local = Path(path_str)
        if not local.is_file():
            raise RuntimeError(f"DORA_BINARY does not point to a file: {local}")
        return local

    # -- installation ----------------------------------------------------------

    async def install(self, environment: BaseEnvironment) -> None:  # type: ignore[override]
        """Install the dora binary into the sandbox.

        Steps:
          1. Ensure the minimal system dependencies are present.
          2. Resolve and upload the local Linux dora binary to
             ``/installed-agent/dora`` and make it executable.
          3. Verify it runs via ``dora --version`` and log the output.
        """
        if _HARBOR_IMPORT_ERROR is not None:
            raise RuntimeError(
                "Harbor is not installed/importable in this Python environment: "
                f"{_HARBOR_IMPORT_ERROR}. Run inside the harbor python env."
            ) from _HARBOR_IMPORT_ERROR

        # Ensure dora's runtime dependencies exist inside the container.
        await self.ensure_system_dependencies(environment, self.SYSTEM_DEPENDENCIES)

        local_binary = self._resolve_local_binary()
        self.logger.info("Uploading dora binary from %s", local_binary)

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
        """Run dora against one task instruction inside the sandbox.

        The instruction is written into a random shell environment variable and
        piped to dora's stdin (following the ``claude_code`` pattern), the
        merged output is teed to ``/logs/agent/dora.txt``. Passing the
        instruction via stdin (rather than as a command-line positional
        argument) avoids Go's ``flag`` parser treating an instruction that
        starts with ``-`` (e.g. a Markdown list item) as a flag.
        """
        if _HARBOR_IMPORT_ERROR is not None:
            raise RuntimeError(
                "Harbor is not installed/importable in this Python environment: "
                f"{_HARBOR_IMPORT_ERROR}."
            ) from _HARBOR_IMPORT_ERROR

        # Build CLI flags (e.g. "-q -m trust/deepseek-v4-flash").
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
        instruction_shell_var = "dora_instruction_" + uuid.uuid4().hex
        instruction_env_var = instruction_shell_var.upper()
        run_env[instruction_env_var] = instruction

        command = (
            "export PATH=\"$HOME/.local/bin:$PATH\"; "
            f"{instruction_shell_var}=\"${instruction_env_var}\"; "
            f"unset {instruction_env_var}; "
            "set -o pipefail; "
            f'printf "%s" "${{{instruction_shell_var}}}" | '
            f"{BINARY_PATH} {extra_flags} 2>&1 | stdbuf -oL tee /logs/agent/dora.txt"
        )

        try:
            result = await self.exec_as_agent(environment, command=command, env=run_env)
            # NOTE: token/cost accounting is intentionally left empty for now.
            # It could be parsed out of /logs/agent/dora.txt later; the agent
            # context tolerates all-None fields (AgentContext.is_empty()).
            _ = result
        except Exception as exc:  # NonZeroAgentExitCodeError and friends
            # The full transcript lives in /logs/agent/dora.txt for post-hoc
            # inspection regardless of exit status.
            self.logger.warning("dora run exited with an error: %s", exc)
            raise
