"""Run with: python -m unittest discover -s scripts/eval -p 'test_*.py'.

Use the Python environment containing Harbor. No containers or model calls run.
"""

import asyncio
import shlex
import tempfile
import unittest
from pathlib import Path
from unittest.mock import AsyncMock

from harbor.agents.factory import AgentFactory
from harbor.models.trial.config import AgentConfig

from dora_tb import BINARY_PATH, DoraAgent


class DoraModelSelectionTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix="dora-tb-test-")
        self.addCleanup(directory.cleanup)
        self.logs_dir = Path(directory.name)

    def agent(self, **kwargs):
        return DoraAgent(logs_dir=self.logs_dir, version="test", **kwargs)

    def test_model_name_drives_flags_and_metadata(self):
        agent = self.agent(model_name="openrouter/auto")
        self.assertEqual(
            shlex.split(agent.build_cli_flags()), ["--model", "openrouter/auto"]
        )
        info = agent.to_agent_info()
        self.assertEqual(info.name, "aipymini")
        self.assertEqual(info.model_info.provider, "openrouter")
        self.assertEqual(info.model_info.name, "auto")
        self.assertNotIn("model", [flag.kwarg for flag in DoraAgent.CLI_FLAGS])

    def test_quiet_flag_is_preserved(self):
        agent = self.agent(model_name="trust/hy4-preview", quiet=True)
        self.assertEqual(
            shlex.split(agent.build_cli_flags()),
            ["--model", "trust/hy4-preview", "--quiet"],
        )

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
            import_path="dora_tb:DoraAgent",
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

    def test_run_forwards_model_name_to_dora(self):
        agent = self.agent(model_name="trust/hy4-preview")
        agent.exec_as_agent = AsyncMock()
        asyncio.run(agent.run("Inspect the repository", object(), None))
        agent.exec_as_agent.assert_awaited_once()
        command = agent.exec_as_agent.call_args.kwargs["command"]
        dora_argv = shlex.split(command.split(" | ", 1)[1].split(" > ", 1)[0])
        self.assertEqual(dora_argv, [BINARY_PATH, "--model", agent.model_name])


if __name__ == "__main__":
    unittest.main()
