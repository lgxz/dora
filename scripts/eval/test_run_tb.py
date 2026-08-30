"""Exercise the wrapper with a fake Harbor; no containers or network required."""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


class RunTBTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix="dora-tb-wrapper-test-")
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
            "import json, os, sys, yaml\n"
            "from pathlib import Path\n"
            "args = sys.argv[1:]\n"
            "config_path = Path(args[args.index('--config') + 1])\n"
            "capture_path = Path(os.environ['TB_TEST_CAPTURE'])\n"
            "assert not capture_path.exists(), 'Harbor must be called only once'\n"
            "capture = {\n"
            "    'args': args, 'path': str(config_path),\n"
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
            "DORA_BINARY": sys.executable,
            # The obsolete environment variable must never select a model.
            "DORA_MODEL": "obsolete/ignored-model",
            "DORA_JOBS_DIR": str(self.root / "jobs"),
            "OPENROUTER_API_KEY": "test-key-not-for-logs",
            "TB_TEST_CAPTURE": str(self.capture),
            "TB_TEST_FAILURE": "",
        }

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
        self.assertNotIn(self.env["OPENROUTER_API_KEY"], result.stdout + result.stderr)
        return result

    def test_generates_minimal_yaml_and_cleans_up(self):
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        capture = json.loads(self.capture.read_text())
        config = capture["config"]
        self.assertEqual(config, {"agents": [{
            "name": "aipymini",
            "import_path": "dora_tb:DoraAgent",
            "model_name": "openrouter/auto",
        }]})
        self.assertNotIn("-m", capture["args"])
        self.assertNotIn("--ak", capture["args"])
        self.assertEqual(capture["args"][-2:], ["-n", "3"])
        self.assertIn("OPENROUTER_API_KEY=test-key-not-for-logs", capture["args"])
        self.assertEqual(capture["mode"], 0o600)
        self.assertEqual(Path(capture["path"]).suffix, ".yaml")
        self.assertFalse(Path(capture["path"]).exists())
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

    def test_missing_model_does_not_fall_back_to_environment(self):
        result = self.run_wrapper([])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("-m", result.stderr)
        self.assertFalse(self.capture.exists())

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

    def test_agent_and_config_overrides_are_rejected(self):
        for option in ("-a", "--agent=other", "--agent-import-path=other:Agent", "-c", "--config=other.yaml"):
            with self.subTest(option=option):
                result = self.run_wrapper(["-m", "openrouter/auto", option])
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.capture.exists())

    def test_run_failure_preserves_exit_code_and_cleans_up(self):
        self.env["TB_TEST_FAILURE"] = "run"
        self.assertEqual(self.run_wrapper().returncode, 23)
        self.assertTrue(self.capture.exists())

    def test_script_runs_without_adjacent_config(self):
        isolated_script = self.root / "run_tb.sh"
        isolated_script.write_text(self.script.read_text())
        self.script = isolated_script
        self.assertEqual(list(self.script.parent.glob("*.yaml")), [])
        result = self.run_wrapper()
        self.assertEqual(result.returncode, 0, result.stderr)
        config = json.loads(self.capture.read_text())["config"]
        self.assertEqual(config["agents"][0]["model_name"], "openrouter/auto")


if __name__ == "__main__":
    unittest.main()
