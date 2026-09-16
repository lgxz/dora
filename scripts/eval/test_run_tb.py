"""Exercise the wrapper with a fake Harbor; no containers or network required."""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from harbor.models.job.config import JobConfig


class RunTBTests(unittest.TestCase):
    DEFAULT_MODEL = "deepseek/deepseek-v4-pro"
    OFFICIAL_DATASET = (
        "terminal-bench/terminal-bench-2-1@"
        "sha256:7d7bdc1cbedad549fc1140404bd4dc45e5fd0ea7c4186773687d177ad3a0699a"
    )

    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix="aipymini-tb-wrapper-test-")
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.temp_root = self.root / "temp"
        self.temp_root.mkdir()
        self.capture = self.root / "capture.json"
        self.script = Path(__file__).with_name("run_tb.sh")
        binary_dir = self.root / "bin"
        binary_dir.mkdir()
        harbor = binary_dir / "harbor"
        harbor.write_text(
            f"#!{sys.executable}\n"
            "import aipymini, json, os, sys, yaml\n"
            "from pathlib import Path\n"
            "args = sys.argv[1:]\n"
            "config_path = Path(args[args.index('--config') + 1])\n"
            "capture_path = Path(os.environ['TB_TEST_CAPTURE'])\n"
            "assert not capture_path.exists(), 'Harbor must be called only once'\n"
            "capture = {\n"
            "    'args': args, 'path': str(config_path),\n"
            "    'adapter_path': aipymini.__file__,\n"
            "    'binary_path': os.environ['AIPYMINI_BINARY'],\n"
            "    'mode': config_path.stat().st_mode & 0o777,\n"
            "    'config': yaml.safe_load(config_path.read_text())}\n"
            "capture_path.write_text(json.dumps(capture))\n"
            "if os.environ.get('TB_TEST_FAILURE') == 'run':\n"
            "    sys.exit(23)\n"
        )
        harbor.chmod(0o700)
        self.env = {
            **os.environ,
            "PATH": f"{binary_dir}{os.pathsep}{os.environ['PATH']}",
            "TMPDIR": str(self.temp_root),
            "AIPYMINI_BINARY": sys.executable,
            "AIPYMINI_JOBS_DIR": str(self.root / "jobs"),
            "DEEPSEEK_API_KEY": "test-deepseek-key-not-for-logs",
            "OPENROUTER_API_KEY": "test-key-not-for-logs",
            "TB_TEST_CAPTURE": str(self.capture),
            "TB_TEST_FAILURE": "",
        }
        self.env.pop("AIPYMINI_DATASET", None)

    def run_wrapper(self, args=None):
        if args is None:
            args = ["-m", "openrouter/auto", "-n", "3"]
        result = subprocess.run(
            ["bash", str(self.script), *args],
            env=self.env,
            capture_output=True,
            text=True,
            timeout=15,
        )
        self.assertEqual(list(self.temp_root.iterdir()), [])
        output = result.stdout + result.stderr
        self.assertNotIn(self.env["DEEPSEEK_API_KEY"], output)
        self.assertNotIn(self.env["OPENROUTER_API_KEY"], output)
        return result

    def test_generates_minimal_yaml_and_cleans_up(self):
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        capture = json.loads(self.capture.read_text())
        config = capture["config"]
        self.assertEqual(config, {
            "datasets": [{
                "name": "terminal-bench/terminal-bench-2-1",
                "ref": self.OFFICIAL_DATASET.split("@", 1)[1],
            }],
            "agents": [{
                "name": "aipymini",
                "import_path": "aipymini:AIPyMiniAgent",
                "model_name": "openrouter/auto",
            }],
        })
        job_config = JobConfig.model_validate(config)
        self.assertEqual(job_config.datasets[0].name, "terminal-bench/terminal-bench-2-1")
        self.assertEqual(job_config.datasets[0].ref, self.OFFICIAL_DATASET.split("@", 1)[1])
        self.assertNotIn("-m", capture["args"])
        self.assertNotIn("--ak", capture["args"])
        self.assertNotIn("-d", capture["args"])
        self.assertNotIn("--dataset", capture["args"])
        self.assertEqual(capture["args"][-2:], ["-n", "3"])
        self.assertIn("OPENROUTER_API_KEY=test-key-not-for-logs", capture["args"])
        self.assertEqual(capture["mode"], 0o600)
        self.assertEqual(Path(capture["path"]).suffix, ".yaml")
        self.assertFalse(Path(capture["path"]).exists())
        self.assertEqual(Path(capture["adapter_path"]).name, "aipymini.py")
        self.assertEqual(Path(capture["binary_path"]).name, "aipymini")
        self.assertFalse(Path(capture["adapter_path"]).exists())
        self.assertFalse(Path(capture["binary_path"]).exists())
        self.assertNotIn("dora", json.dumps(capture).lower())
        self.assertNotIn("test-key-not-for-logs", json.dumps(config))

    def test_model_is_yaml_quoted(self):
        model = "openrouter/team's-\"profile\""
        result = self.run_wrapper(["--model", model])
        self.assertEqual(result.returncode, 0, result.stderr)
        config = json.loads(self.capture.read_text())["config"]
        self.assertEqual(config["agents"][0]["model_name"], model)

    def test_equals_form_is_consumed(self):
        result = self.run_wrapper(["--model=openrouter/auto"])
        self.assertEqual(result.returncode, 0, result.stderr)
        capture = json.loads(self.capture.read_text())
        self.assertNotIn("--model=openrouter/auto", capture["args"])
        self.assertEqual(capture["config"]["agents"][0]["model_name"], "openrouter/auto")

    def test_uses_default_model_when_model_is_omitted(self):
        result = self.run_wrapper([])
        self.assertEqual(result.returncode, 0, result.stderr)
        capture = json.loads(self.capture.read_text())
        self.assertEqual(
            capture["config"]["agents"][0]["model_name"],
            self.DEFAULT_MODEL,
        )
        self.assertIn(
            "DEEPSEEK_API_KEY=test-deepseek-key-not-for-logs",
            capture["args"],
        )
        self.assertNotIn(
            "OPENROUTER_API_KEY=test-key-not-for-logs",
            capture["args"],
        )

    def test_invalid_model_arguments_fail_before_running_harbor(self):
        cases = [
            ["-m"], ["--model="], ["-m", "openrouter"], ["-m", "/auto"],
            ["-m", "openrouter/"], ["-m", "openrouter/team/auto"],
            ["-m", "openrouter/auto model"], ["-m", "--print-config"],
            ["-m", "openrouter/auto", "-m", "trust/hy4-preview"],
        ]
        for args in cases:
            with self.subTest(args=args):
                self.assertNotEqual(self.run_wrapper(args).returncode, 0)
                self.assertFalse(self.capture.exists())

    def test_provider_controls_injected_key(self):
        self.env["TRUST_API_KEY"] = "test-trust-key"
        result = self.run_wrapper(["-m", "trust/hy4-preview"])
        self.assertEqual(result.returncode, 0, result.stderr)
        capture = json.loads(self.capture.read_text())
        self.assertIn("TRUST_API_KEY=test-trust-key", capture["args"])
        self.assertNotIn("OPENROUTER_API_KEY=test-key-not-for-logs", capture["args"])
        self.assertEqual(capture["config"]["agents"][0]["model_name"], "trust/hy4-preview")
        self.assertNotIn("test-trust-key", result.stdout + result.stderr)

    def test_agent_dataset_and_config_overrides_are_rejected(self):
        for option in (
            "-a", "--agent=other", "--agent-import-path=other:Agent",
            "-c", "--config=other.yaml", "-d", "--dataset=other/dataset",
        ):
            with self.subTest(option=option):
                result = self.run_wrapper(["-m", "openrouter/auto", option])
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.capture.exists())

    def test_run_failure_preserves_exit_code_and_cleans_up(self):
        self.env["TB_TEST_FAILURE"] = "run"
        self.assertEqual(self.run_wrapper().returncode, 23)
        self.assertTrue(self.capture.exists())

    def test_default_jobs_directory_is_under_home(self):
        self.env.pop("AIPYMINI_JOBS_DIR")
        self.env["HOME"] = str(self.root / "home")
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        args = json.loads(self.capture.read_text())["args"]
        self.assertEqual(args[args.index("-o") + 1], str(Path(self.env["HOME"]) / "jobs"))

    def test_dataset_can_be_explicitly_overridden(self):
        self.env["AIPYMINI_DATASET"] = "example/custom@sha256:test"
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        capture = json.loads(self.capture.read_text())
        args = capture["args"]
        self.assertNotIn("-d", args)
        self.assertEqual(
            capture["config"]["datasets"],
            [{"name": "example/custom", "ref": "sha256:test"}],
        )

    def test_invalid_dataset_override_fails_before_running_harbor(self):
        for dataset in ("example/custom", "/custom@latest", "example/@latest", "example/custom@"):
            with self.subTest(dataset=dataset):
                self.env["AIPYMINI_DATASET"] = dataset
                self.assertNotEqual(self.run_wrapper().returncode, 0)
                self.assertFalse(self.capture.exists())

    def test_script_runs_without_adjacent_config(self):
        isolated_script = self.root / "run_tb.sh"
        isolated_script.write_text(self.script.read_text())
        isolated_adapter = self.root / "aipymini.py"
        isolated_adapter.write_text(self.script.with_name("aipymini.py").read_text())
        self.script = isolated_script
        self.assertEqual(list(self.script.parent.glob("*.yaml")), [])
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        config = json.loads(self.capture.read_text())["config"]
        self.assertEqual(config["agents"][0]["model_name"], "openrouter/auto")


if __name__ == "__main__":
    unittest.main()
