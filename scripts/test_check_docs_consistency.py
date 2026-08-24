import json
from pathlib import Path
import subprocess
import tempfile
import unittest

import check_docs_consistency as checks


def git_track(root: Path, *paths: str) -> None:
    subprocess.run(["git", "init", "-q"], cwd=root, check=True)
    if paths:
        subprocess.run(["git", "add", "--", *paths], cwd=root, check=True)


class DocsConsistencyTest(unittest.TestCase):
    def test_release_pointer_matches_record_without_consulting_head(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs/releases").mkdir(parents=True)
            sha = "a" * 40
            (root / "docs/status.md").write_text(
                f"- Current deployed release: `v1.2.3`\n- Current deployed revision: `{sha}`\n",
                encoding="utf-8",
            )
            (root / "docs/releases/v1.2.3-qualification.md").write_text(
                f"- Release: `v1.2.3`\n- Revision: `{sha}`\n", encoding="utf-8"
            )
            checks.validate_release_pointer(root)

    def test_test_verified_capability_requires_test_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "impl.go").write_text("package example\n", encoding="utf-8")
            (root / "docs/status.md").write_text(
                "| Capability | State | Evidence |\n| --- | --- | --- |\n| Runtime | test-verified | evidence |\n",
                encoding="utf-8",
            )
            contract = {
                "version": 1,
                "states": sorted(checks.CAPABILITY_STATES),
                "capabilities": [{"name": "Runtime", "state": "test-verified", "implementation": ["impl.go"], "tests": []}],
            }
            (root / "docs/capabilities-v1.json").write_text(json.dumps(contract), encoding="utf-8")
            git_track(root, "impl.go")
            with self.assertRaisesRegex(checks.ContractError, "requires test evidence"):
                checks.validate_capabilities(root)

    def test_capability_state_enum_is_fixed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "docs/status.md").write_text("", encoding="utf-8")
            contract = {"version": 1, "states": ["anything"], "capabilities": []}
            (root / "docs/capabilities-v1.json").write_text(json.dumps(contract), encoding="utf-8")
            git_track(root)
            with self.assertRaisesRegex(checks.ContractError, "fixed capability enum"):
                checks.validate_capabilities(root)

    def test_test_verified_rejects_non_test_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "impl.go").write_text("package example\n", encoding="utf-8")
            (root / "docs/status.md").write_text(
                "| Capability | State | Evidence |\n| --- | --- | --- |\n| Runtime | test-verified | evidence |\n",
                encoding="utf-8",
            )
            contract = {
                "version": 1,
                "states": sorted(checks.CAPABILITY_STATES),
                "capabilities": [{"name": "Runtime", "state": "test-verified", "implementation": ["impl.go"], "tests": ["impl.go"]}],
            }
            (root / "docs/capabilities-v1.json").write_text(json.dumps(contract), encoding="utf-8")
            git_track(root, "impl.go")
            with self.assertRaisesRegex(checks.ContractError, "must name test files"):
                checks.validate_capabilities(root)

    def test_relative_markdown_link_must_exist(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "README.md"
            source.write_text("[missing](docs/missing.md)\n", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, "relative Markdown link"):
                checks.validate_markdown_links(root, [source])

    def test_inline_image_target_must_exist(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "README.md"
            source.write_text("![diagram](docs/missing.png)\n", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, "relative Markdown link"):
                checks.validate_markdown_links(root, [source])

    def test_reference_style_target_must_exist(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "README.md"
            source.write_text("[guide][docs]\n\n[docs]: docs/missing.md\n", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, "relative Markdown link"):
                checks.validate_markdown_links(root, [source])

    def test_reference_style_link_requires_definition(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "README.md"
            source.write_text("[guide][missing]\n", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, "reference is undefined"):
                checks.validate_markdown_links(root, [source])

    def test_capability_evidence_must_be_tracked(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "impl.go").write_text("package example\n", encoding="utf-8")
            (root / "docs/status.md").write_text(
                "| Capability | State | Evidence |\n| --- | --- | --- |\n| Runtime | implemented | impl.go |\n",
                encoding="utf-8",
            )
            contract = {
                "version": 1,
                "states": sorted(checks.CAPABILITY_STATES),
                "capabilities": [{"name": "Runtime", "state": "implemented", "implementation": ["impl.go"], "tests": []}],
            }
            (root / "docs/capabilities-v1.json").write_text(json.dumps(contract), encoding="utf-8")
            git_track(root)
            with self.assertRaisesRegex(checks.ContractError, "tracked by git"):
                checks.validate_capabilities(root)

    def test_capability_evidence_rejects_escaping_symlink(self) -> None:
        with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryDirectory() as outside:
            root = Path(directory)
            (root / "docs").mkdir()
            target = Path(outside) / "secret.go"
            target.write_text("secret\n", encoding="utf-8")
            (root / "evidence.go").symlink_to(target)
            (root / "docs/status.md").write_text(
                "| Capability | State | Evidence |\n| --- | --- | --- |\n| Runtime | implemented | evidence.go |\n",
                encoding="utf-8",
            )
            contract = {
                "version": 1,
                "states": sorted(checks.CAPABILITY_STATES),
                "capabilities": [{"name": "Runtime", "state": "implemented", "implementation": ["evidence.go"], "tests": []}],
            }
            (root / "docs/capabilities-v1.json").write_text(json.dumps(contract), encoding="utf-8")
            git_track(root, "evidence.go")
            with self.assertRaisesRegex(checks.ContractError, "escapes the repository"):
                checks.validate_capabilities(root)

    def test_release_pointer_rejects_duplicate_fields(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            sha = "a" * 40
            (root / "docs/status.md").write_text(
                f"- Current deployed release: `v1.2.3`\n- Current deployed release: `v1.2.3`\n- Current deployed revision: `{sha}`\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(checks.ContractError, "exactly one semver"):
                checks.validate_release_pointer(root)

    def test_release_pointer_rejects_non_semver_tag(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "docs/status.md").write_text(
                f"- Current deployed release: `../../secret`\n- Current deployed revision: `{'a' * 40}`\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(checks.ContractError, "exactly one semver"):
                checks.validate_release_pointer(root)

    def test_malformed_capability_json_is_contract_error(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "docs/capabilities-v1.json").write_text("{secret", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, "invalid JSON") as raised:
                checks.validate_capabilities(root)
            self.assertNotIn("secret", str(raised.exception))

    def test_malformed_codemap_is_contract_error(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "codemap.md").write_text("malformed", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, "required sections"):
                checks.validate_codemap(root)

    def test_default_failure_does_not_print_secret_value(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "backend/internal/runtimeconfig").mkdir(parents=True)
            (root / ".env.example").write_text("TOKEN=safe-example\n", encoding="utf-8")
            contract = {"version": 1, "defaults": {"TOKEN": {"runtime": "", "compose": "", "example": "safe-example", "secret": True}}}
            (root / "backend/internal/runtimeconfig/defaults-v1.json").write_text(json.dumps(contract), encoding="utf-8")
            compose = {"services": {"api": {"environment": {"TOKEN": "do-not-print"}}, "opencode": {"environment": {}}}}
            with self.assertRaises(checks.ContractError) as raised:
                checks.validate_defaults(root, compose)
            self.assertNotIn("do-not-print", str(raised.exception))

    def test_defaults_compare_case_sensitive_command_values(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "backend/internal/runtimeconfig").mkdir(parents=True)
            (root / ".env.example").write_text("OPEN_PILOT_COMMAND_DENYLIST=rm\n", encoding="utf-8")
            contract = {
                "version": 1,
                "defaults": {
                    "OPEN_PILOT_COMMAND_DENYLIST": {
                        "runtime": "rm",
                        "compose": "rm",
                        "example": "rm",
                    }
                },
                "listeners": {},
            }
            (root / "backend/internal/runtimeconfig/defaults-v1.json").write_text(json.dumps(contract), encoding="utf-8")
            compose = {
                "services": {
                    "api": {"environment": {"OPEN_PILOT_COMMAND_DENYLIST": "RM"}, "ports": []},
                    "opencode": {"environment": {}},
                }
            }
            with self.assertRaisesRegex(checks.ContractError, "rendered critical default mismatch"):
                checks.validate_defaults(root, compose)

    def test_malformed_url_port_is_sanitized_contract_error(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "backend/internal/runtimeconfig").mkdir(parents=True)
            (root / ".env.example").write_text(
                "SWITCHYARD_API_URL=http://127.0.0.1:secret\nSWITCHYARD_BIND_HOST=127.0.0.1\nAPI_PORT=18080\n",
                encoding="utf-8",
            )
            contract = {
                "version": 1,
                "defaults": {},
                "listeners": {"control": {"bind_env": "SWITCHYARD_BIND_HOST", "port_env": "API_PORT", "target": 8080}},
            }
            (root / "backend/internal/runtimeconfig/defaults-v1.json").write_text(json.dumps(contract), encoding="utf-8")
            compose = {"services": {"api": {"environment": {}, "ports": [{"target": 8080, "host_ip": "127.0.0.1", "published": "18080"}]}, "opencode": {"environment": {}}}}
            with self.assertRaisesRegex(checks.ContractError, "URL or port is malformed") as raised:
                checks.validate_defaults(root, compose)
            self.assertNotIn("secret", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
