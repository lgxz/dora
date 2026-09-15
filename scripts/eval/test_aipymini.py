"""Run with: python -m unittest discover -s scripts/eval -p 'test_*.py'.

Use the Python environment containing Harbor. No containers or model calls run.
"""

import asyncio
import json
import shlex
import tempfile
import unittest
from pathlib import Path
from unittest.mock import AsyncMock

from harbor.agents.factory import AgentFactory
from harbor.models.agent.context import AgentContext
from harbor.models.trial.config import AgentConfig
from harbor.models.trajectories import Trajectory

from aipymini import (
    AIPyMiniAgent,
    BINARY_PATH,
    TRACE_PATH,
    TRAJECTORY_PATH,
)


class AIPyMiniModelSelectionTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix="aipymini-tb-test-")
        self.addCleanup(directory.cleanup)
        self.logs_dir = Path(directory.name)

    def agent(self, **kwargs):
        return AIPyMiniAgent(logs_dir=self.logs_dir, version="test", **kwargs)

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
                "--trace-file",
                TRACE_PATH,
                "--model",
                agent.model_name,
            ],
        )
        self.assertIn("> /logs/agent/run.txt 2>&1", command)

    def test_populates_harbor_context_from_trace(self):
        agent = self.agent(model_name="trust/hy4-preview")
        (self.logs_dir / Path(TRACE_PATH).name).write_text(json.dumps({
            "schema_version": 1,
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
        agent = self.agent(model_name="trust/hy4-preview")
        trace_path = self.logs_dir / Path(TRACE_PATH).name
        trace_path.write_text(json.dumps({
            "schema_version": 1,
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
        }), encoding="utf-8")

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
        agent = self.agent(model_name="trust/hy4-preview")
        (self.logs_dir / Path(TRACE_PATH).name).write_text(json.dumps({
            "schema_version": 1,
            "user": "test",
            "rounds": [{
                "assistant": {"tool_calls": [{
                    "id": "call-1", "name": "bash", "input": "{"
                }]},
                "tools": [{"tool_call_id": "call-1", "content": "failed"}],
            }],
        }), encoding="utf-8")

        agent.populate_context_post_run(AgentContext())

        trajectory = Trajectory.model_validate_json(
            (self.logs_dir / Path(TRAJECTORY_PATH).name).read_text(encoding="utf-8")
        )
        self.assertEqual(trajectory.steps[1].tool_calls[0].arguments, {"_raw": "{"})

    def test_missing_trace_leaves_context_empty(self):
        agent = self.agent(model_name="trust/hy4-preview")
        context = AgentContext()
        agent.populate_context_post_run(context)
        self.assertTrue(context.is_empty())


if __name__ == "__main__":
    unittest.main()
