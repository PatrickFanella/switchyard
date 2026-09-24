import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

import check_docs_consistency as checks


def git_track(root: Path, *paths: str) -> None:
    subprocess.run(["git", "init", "-q"], cwd=root, check=True)
    if paths:
        subprocess.run(["git", "add", "--", *paths], cwd=root, check=True)


GIT_IDENTITY = {
    "GIT_AUTHOR_NAME": "Docs Test",
    "GIT_AUTHOR_EMAIL": "docs@example.invalid",
    "GIT_COMMITTER_NAME": "Docs Test",
    "GIT_COMMITTER_EMAIL": "docs@example.invalid",
}


def git_tag(root: Path, *tags: str) -> None:
    env = {**__import__("os").environ, **GIT_IDENTITY}
    subprocess.run(["git", "init", "-q"], cwd=root, check=True)
    subprocess.run(["git", "commit", "-q", "--allow-empty", "-m", "tagged"], cwd=root, check=True, env=env)
    for tag in tags:
        subprocess.run(["git", "tag", tag], cwd=root, check=True, env=env)


def write_release_pointer(root: Path, tag: str, extra: str = "", records: tuple[str, ...] = ()) -> None:
    (root / "docs/releases").mkdir(parents=True, exist_ok=True)
    sha = "a" * 40
    (root / "docs/status.md").write_text(
        f"- Current deployed release: `{tag}`\n- Current deployed revision: `{sha}`\n{extra}",
        encoding="utf-8",
    )
    for record in (tag, *records):
        (root / f"docs/releases/{record}-qualification.md").write_text(
            f"- Release: `{record}`\n- Revision: `{sha}`\n", encoding="utf-8"
        )


def write_tracked_issues(root: Path, rows: str, blockers: str = "") -> None:
    (root / "docs").mkdir(exist_ok=True)
    (root / "docs/status.md").write_text(
        "# Status\n\n## Active release blockers\n\n"
        + blockers
        + "\n## Tracked issues\n\n| Issue | State | Observed | Role |\n| --- | --- | --- | --- |\n"
        + rows
        + "\n## Active references\n",
        encoding="utf-8",
    )
    (root / "docs/ui-feature-migration.md").write_text("# UI\n", encoding="utf-8")


class DocsConsistencyTest(unittest.TestCase):
    def test_release_pointer_rejects_newer_qualification_record(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_release_pointer(root, "v1.2.3", records=("v1.3.0",))
            git_track(root)
            with self.assertRaisesRegex(checks.ContractError, "older than qualification record"):
                checks.validate_release_pointer_currency(root)

    def test_release_pointer_requires_newer_unqualified_tags_to_be_declared(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_release_pointer(root, "v1.2.3")
            git_tag(root, "v1.2.3", "v1.3.0", "v1.3.1")
            env = {**__import__("os").environ, **GIT_IDENTITY}
            subprocess.run(["git", "commit", "-q", "--allow-empty", "-m", "later"], cwd=root, check=True, env=env)
            with self.assertRaisesRegex(checks.ContractError, "not declared: v1.3.0, v1.3.1"):
                checks.validate_release_pointer_currency(root)

    def test_release_pointer_accepts_declared_unqualified_tags(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_release_pointer(
                root, "v1.2.3", "- Release tags without a qualification record: `v1.3.0`, `v1.3.1`\n"
            )
            git_tag(root, "v1.2.3", "v1.3.0", "v1.3.1")
            checks.validate_release_pointer_currency(root)

    def test_release_pointer_rejects_declaring_a_qualified_or_older_tag_as_unqualified(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_release_pointer(root, "v1.2.3", "- Release tags without a qualification record: `v1.2.3`\n")
            git_tag(root, "v1.2.3")
            with self.assertRaisesRegex(checks.ContractError, "has a qualification record"):
                checks.validate_release_pointer_currency(root)
            write_release_pointer(root, "v1.2.3", "- Release tags without a qualification record: `v1.0.0`\n")
            with self.assertRaisesRegex(checks.ContractError, "not newer than the deployed pointer"):
                checks.validate_release_pointer_currency(root)

    def test_release_pointer_rejects_declared_tag_that_does_not_exist(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_release_pointer(root, "v1.2.3", "- Release tags without a qualification record: `v9.9.9`\n")
            git_tag(root, "v1.2.3")
            with self.assertRaisesRegex(checks.ContractError, "do not exist: v9.9.9"):
                checks.validate_release_pointer_currency(root)

    def test_release_pointer_currency_skips_tag_enumeration_outside_git(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_release_pointer(root, "v1.2.3")
            (root / ".git_archival.txt").write_text(f"node: {'b' * 40}\n", encoding="utf-8")
            checks.validate_release_pointer_currency(root)

    def test_release_pointer_currency_skips_tag_checks_in_shallow_clone(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            origin = Path(directory) / "origin"
            origin.mkdir()
            env = {**__import__("os").environ, **GIT_IDENTITY}
            write_release_pointer(origin, "v1.2.3", "- Release tags without a qualification record: `v1.3.0`\n")
            git_tag(origin, "v1.2.3")
            subprocess.run(["git", "add", "-A"], cwd=origin, check=True)
            subprocess.run(["git", "commit", "-q", "-m", "docs"], cwd=origin, check=True, env=env)
            subprocess.run(["git", "commit", "-q", "--allow-empty", "-m", "second"], cwd=origin, check=True, env=env)
            subprocess.run(["git", "tag", "v1.3.0"], cwd=origin, check=True, env=env)
            clone = Path(directory) / "clone"
            subprocess.run(
                ["git", "clone", "-q", "--depth", "1", "--no-tags", f"file://{origin}", str(clone)], check=True
            )
            self.assertIsNone(checks.repository_tags(clone))
            checks.validate_release_pointer_currency(clone)
            # The full checkout still enforces declared-tag existence.
            write_release_pointer(origin, "v1.2.3", "- Release tags without a qualification record: `v1.3.0`, `v9.9.9`\n")
            with self.assertRaisesRegex(checks.ContractError, "do not exist: v9.9.9"):
                checks.validate_release_pointer_currency(origin)

    def test_release_pointer_exempts_the_candidate_tag_at_head(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = {**__import__("os").environ, **GIT_IDENTITY}
            write_release_pointer(root, "v1.2.3")
            git_tag(root, "v1.2.3")
            subprocess.run(["git", "commit", "-q", "--allow-empty", "-m", "candidate"], cwd=root, check=True, env=env)
            subprocess.run(["git", "tag", "v1.3.0"], cwd=root, check=True, env=env)
            checks.validate_release_pointer_currency(root)
            subprocess.run(["git", "commit", "-q", "--allow-empty", "-m", "later"], cwd=root, check=True, env=env)
            with self.assertRaisesRegex(checks.ContractError, "not declared: v1.3.0"):
                checks.validate_release_pointer_currency(root)

    def test_semver_prerelease_sorts_below_final_release(self) -> None:
        self.assertLess(checks.semver_key("v1.3.0-rc.1"), checks.semver_key("v1.3.0"))
        self.assertLess(checks.semver_key("v1.2.9"), checks.semver_key("v1.10.0"))

    def test_issue_reference_must_be_tracked(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_tracked_issues(root, "| #14 | open | 2026-09-23 | qualification |\n")
            (root / "docs/roadmap.md").write_text("# Roadmap\n\n## Open work\n\n1. Finish #99.\n", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, r"docs/roadmap.md: cites issue\(s\) missing .*#99"):
                checks.validate_issue_references(root)

    def test_open_work_may_not_cite_closed_issue_as_open(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_tracked_issues(
                root,
                "| #14 | open | 2026-09-23 | qualification |\n| #16 | closed | 2026-09-23 | alert owner |\n",
            )
            (root / "docs/roadmap.md").write_text(
                "# Roadmap\n\n## Open work\n\n1. Assign an alert owner (#16).\n2. Qualify main (#14).\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(checks.ContractError, r"cites closed issue\(s\) without marking them closed: #16"):
                checks.validate_issue_references(root)
            (root / "docs/roadmap.md").write_text(
                "# Roadmap\n\n## Open work\n\n1. Qualify main (#14). Its former blocker #16 is closed.\n",
                encoding="utf-8",
            )
            checks.validate_issue_references(root)

    def test_open_work_continuation_lines_belong_to_their_item(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_tracked_issues(
                root,
                "| #14 | open | 2026-09-23 | qualification |\n| #16 | closed | 2026-09-23 | alert owner |\n",
                blockers="1. Qualify main (#14).\n   The alert drill #16 is\n   closed.\n",
            )
            (root / "docs/roadmap.md").write_text("# Roadmap\n\n## Open work\n\n- nothing\n", encoding="utf-8")
            checks.validate_issue_references(root)

    def test_tracked_issue_rows_are_validated(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write_tracked_issues(root, "| #14 | pending | 2026-09-23 | qualification |\n")
            (root / "docs/roadmap.md").write_text("# Roadmap\n\n## Open work\n", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, "state must be open or closed"):
                checks.validate_issue_references(root)
            write_tracked_issues(root, "| #14 | open | yesterday | qualification |\n")
            with self.assertRaisesRegex(checks.ContractError, "ISO observation date"):
                checks.validate_issue_references(root)
            write_tracked_issues(root, "| #14 | open | 2026-09-23 | a |\n| #14 | closed | 2026-09-23 | b |\n")
            with self.assertRaisesRegex(checks.ContractError, "listed more than once"):
                checks.validate_issue_references(root)

    def test_historical_plan_documents_require_marker(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs/superpowers/plans").mkdir(parents=True)
            (root / "docs/remediation-plan.md").write_text("# Plan\n\nPhase 1 is next.\n", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, "docs/remediation-plan.md: plan must declare historical"):
                checks.validate_historical_markers(root)
            (root / "docs/remediation-plan.md").write_text(
                "# Plan\n\n> Historical plan: retained for context.\n", encoding="utf-8"
            )
            (root / "docs/production-user-stories.md").write_text("# Stories\n", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, "production-user-stories.md"):
                checks.validate_historical_markers(root)
            (root / "docs/production-user-stories.md").write_text("# Stories\n\nHistorical record.\n", encoding="utf-8")
            (root / "docs/superpowers/plans/2026-01-01-thing.md").write_text("# Thing\n", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, "2026-01-01-thing.md"):
                checks.validate_historical_markers(root)

    def test_proof_stubs_must_be_classified_in_status(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "docs/open-pilot-widget-proof.md").write_text("Proof completed.\n", encoding="utf-8")
            (root / "docs/status.md").write_text("# Status\n", encoding="utf-8")
            with self.assertRaisesRegex(checks.ContractError, "not classified: docs/open-pilot-widget-proof.md"):
                checks.validate_fixture_inventory(root)
            (root / "docs/status.md").write_text(
                "# Status\n\n- `docs/open-pilot-widget-proof.md`: Open Pilot fixture, not evidence.\n",
                encoding="utf-8",
            )
            checks.validate_fixture_inventory(root)

    def test_docs_check_runs_from_git_archive(self) -> None:
        repository = Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            tree = subprocess.run(
                ["git", "write-tree"], cwd=repository, check=True, capture_output=True, text=True
            ).stdout.strip()
            commit = subprocess.run(
                ["git", "commit-tree", tree, "-p", "HEAD"],
                cwd=repository,
                check=True,
                input="docs archive test\n",
                capture_output=True,
                text=True,
                env={**__import__("os").environ, "GIT_AUTHOR_NAME": "Docs Test", "GIT_AUTHOR_EMAIL": "docs@example.invalid", "GIT_COMMITTER_NAME": "Docs Test", "GIT_COMMITTER_EMAIL": "docs@example.invalid"},
            ).stdout.strip()
            archive = temporary / "source.tar"
            subprocess.run(
                ["git", "archive", "--format=tar", f"--output={archive}", commit],
                cwd=repository,
                check=True,
            )
            extracted = temporary / "source"
            extracted.mkdir()
            with tarfile.open(archive) as bundle:
                bundle.extractall(extracted, filter="data")
            result = subprocess.run(
                ["make", "docs-check"], cwd=extracted, capture_output=True, text=True
            )
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

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
            git_track(root, "docs/capabilities-v1.json", "impl.go")
            with self.assertRaisesRegex(checks.ContractError, "requires test evidence"):
                checks.validate_capabilities(root)

    def test_capability_state_enum_is_fixed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "docs/status.md").write_text("", encoding="utf-8")
            contract = {"version": 1, "states": ["anything"], "capabilities": []}
            (root / "docs/capabilities-v1.json").write_text(json.dumps(contract), encoding="utf-8")
            git_track(root, "docs/capabilities-v1.json")
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
            git_track(root, "docs/capabilities-v1.json", "impl.go")
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
            git_track(root, "docs/capabilities-v1.json")
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
            git_track(root, "docs/capabilities-v1.json", "evidence.go")
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
            git_track(root, "docs/capabilities-v1.json")
            with self.assertRaisesRegex(checks.ContractError, "invalid JSON") as raised:
                checks.validate_capabilities(root)
            self.assertNotIn("secret", str(raised.exception))

    def test_capability_contract_itself_must_be_tracked(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "docs/capabilities-v1.json").write_text(
                json.dumps({"version": 1, "states": sorted(checks.CAPABILITY_STATES), "capabilities": []}),
                encoding="utf-8",
            )
            git_track(root)
            with self.assertRaisesRegex(checks.ContractError, "contract must be tracked by git"):
                checks.validate_capabilities(root)

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
                "listeners": {"control": {"bind_env": "SWITCHYARD_BIND_HOST", "port_env": "API_PORT", "published_default": 18080, "target": 8080}},
            }
            (root / "backend/internal/runtimeconfig/defaults-v1.json").write_text(json.dumps(contract), encoding="utf-8")
            compose = {"services": {"api": {"environment": {}, "ports": [{"target": 8080, "host_ip": "127.0.0.1", "published": "18080"}]}, "opencode": {"environment": {}}}}
            with self.assertRaisesRegex(checks.ContractError, "URL or port is malformed") as raised:
                checks.validate_defaults(root, compose)
            self.assertNotIn("secret", str(raised.exception))

    def test_listener_published_port_must_match_runtime_contract(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "backend/internal/runtimeconfig").mkdir(parents=True)
            (root / ".env.example").write_text(
                "SWITCHYARD_API_URL=http://127.0.0.1:28080\nSWITCHYARD_BIND_HOST=127.0.0.1\nAPI_PORT=28080\n",
                encoding="utf-8",
            )
            contract = {
                "version": 1,
                "defaults": {},
                "listeners": {
                    "control": {
                        "bind_env": "SWITCHYARD_BIND_HOST",
                        "port_env": "API_PORT",
                        "published_default": 18080,
                        "target": 8080,
                    }
                },
            }
            (root / "backend/internal/runtimeconfig/defaults-v1.json").write_text(json.dumps(contract), encoding="utf-8")
            compose = {
                "services": {
                    "api": {
                        "environment": {},
                        "ports": [{"target": 8080, "host_ip": "127.0.0.1", "published": "28080"}],
                    },
                    "opencode": {"environment": {}},
                }
            }
            with self.assertRaisesRegex(checks.ContractError, "listener default port mismatch"):
                checks.validate_defaults(root, compose)


if __name__ == "__main__":
    unittest.main()
