"""Contract tests for scripts/prepare_verifier.py (no Docker; image builds are operator-run)."""
import hashlib
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import prepare_verifier  # noqa: E402


def git(repo: Path, *args: str) -> str:
    return subprocess.run(["git", "-C", str(repo), *args], check=True, capture_output=True, text=True).stdout.strip()


class PrepareVerifierTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.repo = Path(self.directory.name)
        git(self.repo, "init", "--quiet", "--initial-branch=main")
        git(self.repo, "config", "user.email", "prepare@example.test")
        git(self.repo, "config", "user.name", "Prepare Test")
        git(self.repo, "config", "commit.gpgsign", "false")
        self.profile = prepare_verifier.load_profile()

    def tearDown(self):
        self.directory.cleanup()

    def commit(self, files: dict[str, str]) -> str:
        for name, contents in files.items():
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(contents)
        git(self.repo, "add", "--all")
        git(self.repo, "commit", "--quiet", "-m", "fixture")
        return git(self.repo, "rev-parse", "HEAD")

    def test_profile_contract_is_shared_with_the_verifier(self):
        contract = json.loads(prepare_verifier.PROFILES_PATH.read_text())
        self.assertEqual(contract["version"], 1)
        self.assertEqual(self.profile["commands"], [["make", "verify"]])
        self.assertEqual(self.profile["toolchains"], {"go": "1.26.6", "node": "24", "pnpm": "10.33.0"})
        self.assertTrue(set(self.profile["prepared_inputs"]).issubset(self.profile["inputs"]))
        self.assertEqual(len(self.profile["inputs"]), 11)

    def test_manifest_binds_every_input_at_the_exact_revision(self):
        revision = self.commit({
            "backend/go.mod": "module fixture.test/backend\n\ngo 1.22\n",
            "web/package.json": '{"name":"web","packageManager":"pnpm@10.33.0"}\n',
            "web/pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
        })
        self.commit({"web/pnpm-lock.yaml": "lockfileVersion: '9.0'\n# later change\n"})
        files, contents = prepare_verifier.committed_inputs(self.repo, revision, self.profile)
        self.assertEqual(set(files), set(self.profile["inputs"]))
        self.assertEqual(files["web/pnpm-lock.yaml"], hashlib.sha256(b"lockfileVersion: '9.0'\n").hexdigest())
        self.assertIsNone(files["backend/go.sum"])
        self.assertIsNone(files["pnpm-workspace.yaml"])
        self.assertEqual(set(contents), {"backend/go.mod", "web/package.json", "web/pnpm-lock.yaml"})
        manifest = prepare_verifier.build_manifest(revision, files, "sha256:" + "0" * 64)
        self.assertEqual(manifest["version"], 2)
        self.assertEqual(manifest["profile"], prepare_verifier.PROFILE)
        self.assertEqual(manifest["base_sha"], revision)
        self.assertEqual(manifest["commands"], [["make", "verify"]])

    def test_unsupported_dependency_configuration_is_rejected(self):
        revision = self.commit({"backend/go.mod": "module x\n", "pnpm-workspace.yaml": "packages: []\n"})
        with self.assertRaisesRegex(prepare_verifier.PreparationError, "unsupported dependency configuration: pnpm-workspace.yaml"):
            prepare_verifier.committed_inputs(self.repo, revision, self.profile)

    def test_package_manager_must_match_the_pinned_toolchain(self):
        revision = self.commit({"web/package.json": '{"name":"web","packageManager":"pnpm@9.0.0"}\n'})
        with self.assertRaisesRegex(prepare_verifier.PreparationError, "requires pnpm@10.33.0"):
            prepare_verifier.committed_inputs(self.repo, revision, self.profile)

    def test_unknown_profile_and_revision_are_rejected(self):
        with self.assertRaises(prepare_verifier.PreparationError):
            prepare_verifier.load_profile("go0-unknown")
        with self.assertRaises(subprocess.CalledProcessError):
            prepare_verifier.resolve_revision(self.repo, "does-not-exist")

    def test_dockerfile_prepares_dependencies_outside_verification(self):
        dockerfile = (prepare_verifier.ROOT / "services/verifier/Dockerfile").read_text()
        self.assertIn("pnpm@" + self.profile["toolchains"]["pnpm"], dockerfile)
        self.assertIn("--ignore-scripts", dockerfile)
        self.assertIn("go mod download", dockerfile)
        self.assertIn("USER 1000:1000", dockerfile)
        self.assertNotIn("docker.sock", dockerfile)


if __name__ == "__main__":
    unittest.main()
