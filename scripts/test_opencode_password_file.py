import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
HELPER = ROOT / "services/opencode/password.sh"


class OpenCodePasswordFileTest(unittest.TestCase):
    def run_load(self, path: Path | None, password: str = "") -> subprocess.CompletedProcess[str]:
        env = os.environ.copy()
        env.update({
            "OPENCODE_RUNTIME_UID": str(os.getuid()),
            "OPENCODE_SERVER_PASSWORD": password,
            "OPENCODE_SERVER_PASSWORD_FILE": str(path) if path else "",
        })
        return subprocess.run(
            ["sh", "-c", '. "$1"; load_opencode_password || exit $?; printf "%s" "$OPENCODE_SERVER_PASSWORD"', "sh", str(HELPER)],
            env=env, capture_output=True, text=True, check=False,
        )

    def test_loads_private_password_file(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "password"
            path.write_text("correct-horse-battery-staple-value\n", encoding="utf-8")
            path.chmod(0o600)
            result = self.run_load(path)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, "correct-horse-battery-staple-value")

    def test_rejects_ambiguous_inline_and_file_password(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "password"
            path.write_text("a" * 40 + "\n", encoding="utf-8")
            path.chmod(0o600)
            result = self.run_load(path, "b" * 40)
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn("a" * 20, result.stderr)

    def test_rejects_public_password_file(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "password"
            path.write_text("a" * 40 + "\n", encoding="utf-8")
            path.chmod(0o644)
            result = self.run_load(path)
            self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
