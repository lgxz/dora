"""Run with: python -m unittest discover -s scripts/eval -p 'test_*.py'.

Use the Python environment containing Harbor. No containers or model calls run.
"""

import asyncio
import base64
import json
import shlex
import subprocess
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch

from harbor.agents.factory import AgentFactory
from harbor.models.agent.context import AgentContext
from harbor.models.trial.config import AgentConfig
from harbor.models.trajectories import Trajectory

from aipymini import (
    AIPyMiniAgent,
    BINARY_PATH,
    SESSION_PATH,
    TOOLING_LOG_PATH,
    TRAJECTORY_PATH,
    _trace_from_session,
)


def trace_v2(trace):
    """Build explicit accepted-attempt records for ordinary fixture responses."""
    attempts = []
    for index, round_data in enumerate(trace["rounds"]):
        attempts.append(dict(round_data["assistant"], round_index=index, purpose="agent", recovery=0,
                             disposition="tools", finish_reason="tool_calls", output_budget=32768,
                             usage=round_data.get("usage")))
    if "final" in trace:
        attempts.append(dict(trace["final"], round_index=len(trace["rounds"]), purpose="agent", recovery=0,
                             disposition="final", finish_reason="stop", output_budget=32768))
    return dict(trace, status="completed" if "final" in trace else "failed", attempts=attempts)


def session_jsonl(trace):
    """Render a trace fixture as the JSON Lines session file dora writes."""
    def encode_calls(calls):
        return [dict(id=c["id"], name=c["name"],
                     input_bytes=base64.b64encode(c["input"].encode()).decode()) for c in calls]

    lines = [
        {"t": "header", "v": 1},
        {"t": "turn", "id": 1, "system": trace.get("system", ""), "user": trace["user"], "at": "2026-01-01T00:00:00Z"},
    ]
    for index, round_data in enumerate(trace["rounds"]):
        assistant = round_data["assistant"]
        lines.append({
            "t": "round", "turn": 1, "i": index, "usage": round_data.get("usage"),
            "assistant": dict(role="assistant", content=assistant.get("content", ""),
                              reasoning=assistant.get("reasoning", ""),
                              tool_calls=encode_calls(assistant.get("tool_calls", []))),
            "tools": [dict(role="tool", **tool) for tool in round_data["tools"]],
        })
    for index, attempt in enumerate(trace["attempts"]):
        lines.append({"t": "attempt", "turn": 1, "i": index, "record": attempt})
    if trace["status"] == "completed":
        lines.append({"t": "end", "turn": 1, "status": "completed", "at": "2026-01-01T00:00:01Z"})
    elif trace["status"] == "failed":
        lines.append({"t": "end", "turn": 1, "status": "failed", "error": "boom", "at": "2026-01-01T00:00:01Z"})
    # A child task turn is stored in the same file and must not leak into the
    # parent trajectory or its token totals.
    lines.append({"t": "turn", "id": 2, "parent": 1, "user": "child", "at": "2026-01-01T00:00:02Z"})
    lines.append({"t": "attempt", "turn": 2, "i": 0, "record": dict(
        purpose="agent", recovery=0, disposition="final", content="child",
        usage=dict(input_tokens=999, output_tokens=999, total_tokens=1998))})
    lines.append({"t": "end", "turn": 2, "status": "completed", "at": "2026-01-01T00:00:03Z"})
    return "".join(json.dumps(line) + "\n" for line in lines)


class AIPyMiniModelSelectionTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix="aipymini-tb-test-")
        self.addCleanup(directory.cleanup)
        self.logs_dir = Path(directory.name)

    def agent(self, **kwargs):
        return AIPyMiniAgent(logs_dir=self.logs_dir, version="test", **kwargs)

    def write_session(self, trace):
        (self.logs_dir / Path(SESSION_PATH).name).write_text(session_jsonl(trace), encoding="utf-8")

    def trace_agent(self, **kwargs):
        """Agent with the opt-in trace dump enabled, as run_tb.sh --trace does."""
        with patch.dict("os.environ", {"AIPYMINI_TRACE": "1"}):
            return self.agent(**kwargs)

    def test_model_name_drives_flags_and_metadata(self):
        agent = self.agent(model_name="openrouter/auto")
        self.assertEqual(
            shlex.split(agent.build_cli_flags()), ["--model", "openrouter/auto"]
        )
        info = agent.to_agent_info()
        self.assertEqual(info.name, "aipymini")
        self.assertEqual(info.model_info.provider, "openrouter")
        self.assertEqual(info.model_info.name, "auto")
        self.assertNotIn("model", [flag.kwarg for flag in AIPyMiniAgent.CLI_FLAGS])

    def test_quiet_flag_is_preserved(self):
        agent = self.agent(model_name="trust/hy4-preview", quiet=True)
        self.assertEqual(
            shlex.split(agent.build_cli_flags()),
            ["--model", "trust/hy4-preview", "--quiet"],
        )

    def test_version_omits_underlying_executable_name(self):
        agent = self.agent(model_name="openrouter/auto")
        version = agent.parse_version("1.2.3 (commit abc123, built today)")
        self.assertEqual(version, "1.2.3 (commit abc123, built today)")
        self.assertNotIn("dora", version.lower())

    def test_model_is_shell_quoted(self):
        model = "custom/profile with 'quotes' and $(echo unsafe);"
        agent = self.agent(model_name=model)
        self.assertEqual(shlex.split(agent.build_cli_flags()), ["--model", model])

    def test_max_rounds_is_optional_passthrough(self):
        for env_value, expected in (
            (None, []),
            ("64", ["--max-rounds", "64"]),
            (" 128 ", ["--max-rounds", "128"]),
        ):
            with self.subTest(env_value=env_value):
                if env_value is None:
                    agent = self.agent(model_name="trust/hy4-preview")
                else:
                    with patch.dict("os.environ", {"AIPYMINI_MAX_ROUNDS": env_value}):
                        agent = self.agent(model_name="trust/hy4-preview")
                agent.exec_as_agent = AsyncMock()
                asyncio.run(agent.run("Inspect the repository", object(), None))
                command = agent.exec_as_agent.call_args.kwargs["command"]
                aipymini_argv = shlex.split(command.split(" | ", 1)[1].split(" > ", 1)[0])
                self.assertEqual(
                    [arg for arg in aipymini_argv if arg in ("--max-rounds", "64", "128")],
                    expected,
                )

    def test_invalid_max_rounds_is_rejected(self):
        for value in ("0", "-3", "abc", "3.5", "１２"):
            with self.subTest(value=value), patch.dict("os.environ", {"AIPYMINI_MAX_ROUNDS": value}):
                with self.assertRaisesRegex(ValueError, "AIPYMINI_MAX_ROUNDS"):
                    self.agent(model_name="trust/hy4-preview")

    def test_missing_model_name_is_rejected(self):
        for model in (None, "", "   "):
            with self.subTest(model=model):
                with self.assertRaisesRegex(ValueError, "model_name is required"):
                    self.agent(model_name=model)

    def test_legacy_model_kwarg_is_rejected(self):
        for model_name in (None, "trust/hy4-preview"):
            with self.subTest(model_name=model_name):
                with self.assertRaisesRegex(ValueError, "kwargs.model / --ak model"):
                    self.agent(model_name=model_name, model="openrouter/auto")

    def test_job_config_needs_no_model_kwarg(self):
        config = AgentConfig(
            name="aipymini",
            import_path="aipymini:AIPyMiniAgent",
            model_name="trust/hy4-preview",
        )
        self.assertNotIn("model", config.kwargs)
        agent = AgentFactory.create_agent_from_config(config, logs_dir=self.logs_dir)
        flags = shlex.split(agent.build_cli_flags())
        self.assertEqual(flags, ["--model", config.model_name])
        provider, profile = config.model_name.split("/", 1)
        info = agent.to_agent_info()
        self.assertEqual(info.model_info.provider, provider)
        self.assertEqual(info.model_info.name, profile)

    def test_run_forwards_model_name_to_aipymini(self):
        agent = self.agent(model_name="trust/hy4-preview")
        agent.exec_as_agent = AsyncMock()
        asyncio.run(agent.run("Inspect the repository", object(), None))
        agent.exec_as_agent.assert_awaited_once()
        command = agent.exec_as_agent.call_args.kwargs["command"]
        self.assertNotIn("dora", command.lower())
        aipymini_argv = shlex.split(command.split(" | ", 1)[1].split(" > ", 1)[0])
        self.assertEqual(
            aipymini_argv,
            [
                BINARY_PATH,
                "--model",
                agent.model_name,
            ],
        )
        self.assertNotIn("--session", aipymini_argv)
        self.assertIn("> /logs/agent/run.txt 2>&1", command)

    def test_trace_dump_is_opt_in(self):
        for env_value, expected in (
            (None, []),
            ("0", []),
            ("1", ["--session", SESSION_PATH]),
            ("true", ["--session", SESSION_PATH]),
        ):
            with self.subTest(env_value=env_value):
                if env_value is None:
                    agent = self.agent(model_name="trust/hy4-preview")
                else:
                    with patch.dict("os.environ", {"AIPYMINI_TRACE": env_value}):
                        agent = self.agent(model_name="trust/hy4-preview")
                agent.exec_as_agent = AsyncMock()
                asyncio.run(agent.run("Inspect the repository", object(), None))
                command = agent.exec_as_agent.call_args.kwargs["command"]
                aipymini_argv = shlex.split(command.split(" | ", 1)[1].split(" > ", 1)[0])
                self.assertEqual(
                    [arg for arg in aipymini_argv if arg in ("--session", SESSION_PATH)],
                    expected,
                )

    def test_optional_tooling_skips_apt_when_everything_exists(self):
        agent = self.agent(model_name="trust/hy4-preview")
        environment = SimpleNamespace(
            exec=AsyncMock(return_value=SimpleNamespace(return_code=0))
        )
        agent.exec_as_root = AsyncMock()

        asyncio.run(agent._install_optional_tooling(environment))

        self.assertEqual(environment.exec.await_count, 6)
        agent.exec_as_root.assert_not_awaited()

    def test_optional_tooling_installs_missing_packages_independently(self):
        agent = self.agent(model_name="trust/hy4-preview")

        async def check(*, command, user):
            self.assertEqual(user, "root")
            missing = "ca-certificates.crt" in command or "pip --version" in command
            return SimpleNamespace(return_code=1 if missing else 0)

        environment = SimpleNamespace(exec=AsyncMock(side_effect=check))
        agent.exec_as_root = AsyncMock()

        asyncio.run(agent._install_optional_tooling(environment))

        agent.exec_as_root.assert_awaited_once()
        command = agent.exec_as_root.call_args.kwargs["command"]
        self.assertIn("rm -rf /var/lib/apt/lists/*", command)
        self.assertIn("Acquire::Retries=3", command)
        self.assertIn("for package in ca-certificates python3-pip", command)
        self.assertNotIn("for package in curl", command)
        self.assertIn('${VERSION_CODENAME:-}', command)
        self.assertIn('-t bullseye "$package"', command)
        self.assertIn('failed to install $package', command)
        self.assertIn(f"tooling_log={TOOLING_LOG_PATH}", command)
        self.assertIn('} >>"$tooling_log" 2>&1', command)
        self.assertNotIn(">&2", command)
        self.assertTrue(command.endswith("2>&1; exit 0"))
        syntax = subprocess.run(
            ["bash", "-n", "-c", command],
            capture_output=True,
            text=True,
        )
        self.assertEqual(syntax.returncode, 0, syntax.stderr)

    def test_non_apt_environment_does_not_reinstall_existing_ca_bundle(self):
        agent = self.agent(model_name="trust/hy4-preview")
        environment = SimpleNamespace(
            exec=AsyncMock(side_effect=(
                SimpleNamespace(return_code=1),
                SimpleNamespace(return_code=0),
            ))
        )
        agent.ensure_system_dependencies = AsyncMock()

        asyncio.run(agent._install_optional_tooling(environment))

        agent.ensure_system_dependencies.assert_awaited_once_with(
            environment, agent.SYSTEM_DEPENDENCIES
        )

    def test_tooling_install_failure_does_not_stop_binary_install(self):
        agent = self.agent(model_name="trust/hy4-preview")
        agent.logger = MagicMock()
        agent._install_optional_tooling = AsyncMock(
            side_effect=RuntimeError("package mirror unavailable")
        )
        agent.exec_as_root = AsyncMock()
        environment = SimpleNamespace(default_user=None, upload_file=AsyncMock())
        binary = self.logs_dir / "aipymini"
        binary.write_bytes(b"binary")

        with patch.dict("os.environ", {"AIPYMINI_BINARY": str(binary)}):
            asyncio.run(agent.install(environment))

        environment.upload_file.assert_awaited_once_with(binary, BINARY_PATH)
        self.assertEqual(agent.exec_as_root.await_count, 2)
        self.assertIn("chmod +x", agent.exec_as_root.await_args_list[0].kwargs["command"])
        self.assertEqual(
            agent.exec_as_root.await_args_list[1].kwargs["command"],
            f"{BINARY_PATH} --version",
        )
        agent.logger.debug.assert_called_once()
        agent.logger.warning.assert_not_called()

    def test_run_failure_is_only_debug_logged_and_reraised(self):
        agent = self.agent(model_name="trust/hy4-preview")
        agent.logger = MagicMock()
        agent.exec_as_agent = AsyncMock(side_effect=RuntimeError("agent failed"))

        with self.assertRaisesRegex(RuntimeError, "agent failed"):
            asyncio.run(agent.run("Inspect the repository", object(), None))

        agent.logger.debug.assert_called_once()
        agent.logger.warning.assert_not_called()

    def test_missing_or_invalid_trace_is_only_debug_logged(self):
        agent = self.agent(model_name="trust/hy4-preview")
        agent.logger = MagicMock()

        self.assertIsNone(agent._write_trajectory())
        (self.logs_dir / Path(SESSION_PATH).name).write_text("{")
        self.assertIsNone(agent._write_trajectory())

        self.assertEqual(agent.logger.debug.call_count, 2)
        agent.logger.warning.assert_not_called()

    def test_populates_harbor_context_from_trace(self):
        agent = self.trace_agent(model_name="trust/hy4-preview")
        self.write_session(trace_v2({
            "schema_version": 2,
            "user": "test",
            "rounds": [],
            "final": {
                "content": "done",
                "usage": {
                    "input_tokens": 120,
                    "output_tokens": 30,
                    "total_tokens": 150,
                    "input_details": {"cached_tokens": 25},
                },
            },
        }))
        context = AgentContext()

        agent.populate_context_post_run(context)

        self.assertEqual(context.n_input_tokens, 120)
        self.assertEqual(context.n_output_tokens, 30)
        self.assertEqual(context.n_cache_tokens, 25)
        self.assertIsNone(context.cost_usd)

    def test_converts_parent_trace_to_valid_atif(self):
        agent = self.trace_agent(model_name="trust/hy4-preview")
        self.write_session(trace_v2({
            "schema_version": 2,
            "system": "Use tools carefully.",
            "user": "Inspect the repository",
            "rounds": [{
                "assistant": {
                    "reasoning": "I should inspect files.",
                    "tool_calls": [{
                        "id": "call-1",
                        "name": "bash",
                        "input": '{"command":"pwd"}',
                    }],
                },
                "tools": [{
                    "tool_call_id": "call-1",
                    "content": "/root/project",
                }],
                "usage": {
                    "input_tokens": 100,
                    "output_tokens": 20,
                    "total_tokens": 120,
                    "input_details": {"cached_tokens": 10},
                    "output_details": {"reasoning_tokens": 5},
                },
            }],
            "final": {
                "content": "Done",
                "usage": {
                    "input_tokens": 130,
                    "output_tokens": 5,
                    "total_tokens": 135,
                },
            },
        }))

        agent.populate_context_post_run(AgentContext())

        trajectory_path = self.logs_dir / Path(TRAJECTORY_PATH).name
        trajectory = Trajectory.model_validate_json(
            trajectory_path.read_text(encoding="utf-8")
        )
        self.assertEqual(trajectory.schema_version, "ATIF-v1.7")
        self.assertEqual(trajectory.agent.name, "aipymini")
        self.assertEqual(trajectory.agent.version, "test")
        self.assertEqual(trajectory.agent.model_name, "trust/hy4-preview")
        self.assertEqual([step.source for step in trajectory.steps], [
            "system", "user", "agent", "agent"
        ])
        tool_step = trajectory.steps[2]
        self.assertEqual(tool_step.reasoning_content, "I should inspect files.")
        self.assertEqual(tool_step.tool_calls[0].arguments, {"command": "pwd"})
        self.assertEqual(
            tool_step.observation.results[0].content, "/root/project"
        )
        self.assertEqual(trajectory.final_metrics.total_prompt_tokens, 230)
        self.assertEqual(trajectory.final_metrics.total_completion_tokens, 25)
        self.assertEqual(trajectory.final_metrics.total_cached_tokens, 10)
        self.assertEqual(trajectory.final_metrics.total_steps, 4)

    def test_invalid_tool_arguments_are_preserved_in_atif(self):
        agent = self.trace_agent(model_name="trust/hy4-preview")
        self.write_session(trace_v2({
            "schema_version": 2,
            "user": "test",
            "rounds": [{
                "assistant": {"tool_calls": [{
                    "id": "call-1", "name": "bash", "input": "{"
                }]},
                "tools": [{"tool_call_id": "call-1", "content": "failed"}],
            }],
        }))

        agent.populate_context_post_run(AgentContext())

        trajectory = Trajectory.model_validate_json(
            (self.logs_dir / Path(TRAJECTORY_PATH).name).read_text(encoding="utf-8")
        )
        self.assertEqual(trajectory.steps[1].tool_calls[0].arguments, {"_raw": "{"})

    def test_recovery_attempts_count_once_and_do_not_execute_partial_calls(self):
        agent = self.agent(model_name="deepseek/deepseek-v4-pro")
        failed = dict(round_index=0, purpose="agent", recovery=0, disposition="discarded",
                      finish_reason="output_limit", raw_finish_reason="length", output_budget=32768,
                      content="", reasoning="unfinished", tool_calls=[dict(id="partial", name="bash", input="{")],
                      usage=dict(input_tokens=100, output_tokens=32768, total_tokens=32868))
        final = dict(round_index=0, purpose="agent", recovery=1, disposition="final",
                     finish_reason="stop", output_budget=65536, content="done", reasoning="finished",
                     usage=dict(input_tokens=100, output_tokens=20, total_tokens=120))
        trace = dict(schema_version=2, status="completed", user="solve", rounds=[],
                     attempts=[failed, final], final=final)
        trajectory = agent._trajectory_from_trace(trace)
        self.assertEqual(trajectory.final_metrics.total_prompt_tokens, 200)
        self.assertEqual(trajectory.final_metrics.total_completion_tokens, 32788)
        self.assertEqual(len(trajectory.steps), 3)
        self.assertFalse(trajectory.steps[1].tool_calls)
        self.assertEqual(trajectory.steps[1].extra["unexecuted_tool_calls"][0]["input"], "{")
        self.assertEqual(trajectory.steps[1].reasoning_content, "unfinished")
        self.assertEqual(trajectory.steps[2].reasoning_content, "finished")
        trace.update(status="failed", attempts=[failed])
        trace.pop("final")
        self.assertEqual(agent._trajectory_from_trace(trace).final_metrics.total_completion_tokens, 32768)

    def test_rejects_old_trace_schema(self):
        with self.assertRaisesRegex(ValueError, "schema_version"):
            self.agent(model_name="deepseek/deepseek-v4-pro")._trajectory_from_trace(dict(schema_version=1))

    def test_session_conversion_edge_cases(self):
        def lines(*records):
            return "".join(json.dumps(r) + "\n" for r in records)

        header = {"t": "header", "v": 1}
        turn = {"t": "turn", "id": 1, "user": "go", "at": "2026-01-01T00:00:00Z"}
        round_record = {"t": "round", "turn": 1, "i": 0, "assistant": {
            "role": "assistant",
            "tool_calls": [{"id": "c1", "name": "bash", "input_bytes": base64.b64encode(b"\xff{").decode()}]},
            "tools": [{"role": "tool", "tool_call_id": "c1", "content": "out"}]}
        # No end record: the run was interrupted.
        trace = _trace_from_session(lines(header, turn, round_record))
        self.assertEqual(trace["status"], "incomplete")
        self.assertEqual(trace["rounds"][0]["assistant"]["tool_calls"][0]["input"], "\ufffd{")
        # A torn last line is ignored.
        torn = lines(header, turn, round_record) + '{"t":"attempt","turn":1,"i":0,"rec'
        self.assertEqual(len(_trace_from_session(torn)["rounds"]), 1)
        # The model saw the context preamble in front of the user input; a
        # turn without one reports the input alone.
        self.assertEqual(_trace_from_session(lines(header, turn))["user"], "go")
        with_context = _trace_from_session(lines(header, dict(turn, context="<environment>x</environment>")))
        self.assertEqual(with_context["user"], "<environment>x</environment>\n\ngo")
        # A terminal error end is reported as failed; a changed system prompt wins.
        failed = _trace_from_session(lines(header, dict(turn, system="old"), dict(t="end", turn=1, status="canceled", error="x", system="new")))
        self.assertEqual((failed["status"], failed["system"]), ("failed", "new"))
        for bad in ("", lines({"t": "header", "v": 2}), lines(header), '{"version":5}\n{"x":1}\n', "garbage\nmore\n"):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                _trace_from_session(bad)

    def test_missing_trace_leaves_context_empty(self):
        agent = self.trace_agent(model_name="trust/hy4-preview")
        context = AgentContext()
        agent.populate_context_post_run(context)
        self.assertTrue(context.is_empty())

    def test_trace_disabled_skips_trajectory_and_token_accounting(self):
        agent = self.agent(model_name="trust/hy4-preview")
        self.write_session(trace_v2({
            "schema_version": 2,
            "user": "test",
            "rounds": [],
            "final": {
                "content": "done",
                "usage": {"input_tokens": 120, "output_tokens": 30, "total_tokens": 150},
            },
        }))
        context = AgentContext()

        agent.populate_context_post_run(context)

        self.assertTrue(context.is_empty())
        self.assertFalse((self.logs_dir / Path(TRAJECTORY_PATH).name).exists())


if __name__ == "__main__":
    unittest.main()
