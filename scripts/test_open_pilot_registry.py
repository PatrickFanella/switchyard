import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import skipUnless

from open_pilot_registry import (
    canonical_json,
    load_operator_registry,
    registry_fingerprint,
    prepare_registry_filesystem,
    render_compose_override,
    render_compose_file_set,
    render_runtime_manifest,
    validate_registry_filesystem,
    write_atomic,
)
from validate_production_config import MANAGED_MARKER, MANAGED_MARKER_CONTENT
from resolve_open_pilot_compose_file_set import resolve


class OpenPilotRegistryTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.uid = os.getuid()
        self.gid = os.getgid()

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def repository(self, identifier: str, slug: str) -> dict[str, object]:
        root = self.root / identifier
        source = root / "source"
        source.mkdir(parents=True, mode=0o700)
        subprocess.run(["git", "init", "--initial-branch=main", str(source)], check=True, capture_output=True)
        subprocess.run(["git", "-C", str(source), "remote", "add", "origin", f"https://git.example.test/{slug}.git"], check=True)
        worktrees = source / ".open-pilot-worktrees"
        worktrees.mkdir(mode=0o700)
        marker = worktrees / MANAGED_MARKER
        marker.write_text(MANAGED_MARKER_CONTENT, encoding="utf-8")
        marker.chmod(0o600)
        config = root / "config"
        data = root / "data"
        config.mkdir(mode=0o700)
        data.mkdir(mode=0o700)
        password = root / "password"
        password.write_text("a" * 48 + "\n", encoding="utf-8")
        password.chmod(0o600)
        return {
            "id": identifier, "slug": slug, "aliases": [], "executor_generation": "generation-1",
            "source_path": str(source), "worktree_root": str(worktrees), "config_path": str(config),
            "data_path": str(data), "password_file": str(password),
        }

    def write_registry(self, repositories: list[dict[str, object]]) -> Path:
        path = self.root / "registry.json"
        path.write_text(json.dumps({"version": 1, "generation": "registry-1", "repositories": repositories}), encoding="utf-8")
        path.chmod(0o600)
        return path

    def test_two_repository_render_is_deterministic_and_isolated(self) -> None:
        registry = load_operator_registry(self.write_registry([
            self.repository("switchyard", "subculture-collective/switchyard"),
            self.repository("dsa-seats", "subculture-collective/dsa-seats"),
        ]))
        validate_registry_filesystem(registry, self.uid, self.gid, "https://git.example.test")
        fingerprint = registry_fingerprint(registry)
        manifest = render_runtime_manifest(registry, fingerprint)
        override = render_compose_override(registry, self.root / "runtime.json", fingerprint)
        self.assertEqual(fingerprint, registry_fingerprint(registry))
        self.assertEqual(canonical_json(manifest), canonical_json(render_runtime_manifest(registry, fingerprint)))
        self.assertEqual([item["id"] for item in manifest["repositories"]], ["dsa-seats", "switchyard"])
        services = override["services"]
        first = services["open-pilot-opencode-dsa-seats"]
        second = services["open-pilot-opencode-switchyard"]
        self.assertEqual(first["networks"], ["open-pilot-api-dsa-seats"])
        self.assertEqual(second["networks"], ["open-pilot-api-switchyard"])
        first_sources = {volume["source"] for volume in first["volumes"] if isinstance(volume, dict)}
        second_sources = {volume["source"] for volume in second["volumes"] if isinstance(volume, dict)}
        self.assertFalse(first_sources & second_sources)
        self.assertNotIn("OPENCODE_SERVER_PASSWORD", first["environment"])
        self.assertIn("OPENCODE_SERVER_PASSWORD_FILE", first["environment"])
        api_mounts = {volume["target"]: volume["source"] for volume in services["api"]["volumes"]}
        for repository in registry["repositories"]:
            root = f"/open-pilot/repositories/{repository['id']}"
            self.assertEqual(api_mounts[f"{root}/source"], repository["source_path"])
            self.assertEqual(api_mounts[f"{root}/worktrees"], repository["worktree_root"])

    def test_rejects_alias_collision_before_filesystem_validation(self) -> None:
        first = self.repository("one", "Owner/Repo")
        second = self.repository("two", "other/repo")
        second["aliases"] = ["owner/repo"]
        with self.assertRaisesRegex(ValueError, "ambiguous"):
            load_operator_registry(self.write_registry([first, second]))

    def test_rejects_cross_repository_path_overlap(self) -> None:
        first = self.repository("one", "owner/one")
        second = self.repository("two", "owner/two")
        second["config_path"] = first["config_path"]
        registry = load_operator_registry(self.write_registry([first, second]))
        with self.assertRaisesRegex(ValueError, "overlaps"):
            validate_registry_filesystem(registry, self.uid, self.gid, "https://git.example.test")

    def test_rejects_short_or_public_password_file(self) -> None:
        repository = self.repository("one", "owner/one")
        password = Path(repository["password_file"])
        password.write_text("short\n", encoding="utf-8")
        password.chmod(0o644)
        registry = load_operator_registry(self.write_registry([repository]))
        with self.assertRaisesRegex(ValueError, "password file is unsafe"):
            validate_registry_filesystem(registry, self.uid, self.gid, "https://git.example.test")

    def test_rejects_origin_mismatch_without_exposing_origin(self) -> None:
        repository = self.repository("one", "owner/one")
        source = Path(repository["source_path"])
        subprocess.run(["git", "-C", str(source), "remote", "set-url", "origin", "https://secret@example.invalid/private.git"], check=True)
        registry = load_operator_registry(self.write_registry([repository]))
        with self.assertRaisesRegex(ValueError, "does not exactly match") as raised:
            validate_registry_filesystem(registry, self.uid, self.gid, "https://git.example.test")
        self.assertNotIn("secret", str(raised.exception))

    def test_prepares_all_missing_worktree_roots_only_after_complete_preflight(self) -> None:
        first = self.repository("one", "owner/one")
        second = self.repository("two", "owner/two")
        for repository in (first, second):
            worktree = Path(repository["worktree_root"])
            (worktree / MANAGED_MARKER).unlink()
            worktree.rmdir()
        Path(second["password_file"]).chmod(0o644)
        registry = load_operator_registry(self.write_registry([first, second]))
        with self.assertRaisesRegex(ValueError, "password file is unsafe"):
            prepare_registry_filesystem(registry, self.uid, self.gid, "https://git.example.test")
        self.assertFalse(Path(first["worktree_root"]).exists())
        Path(second["password_file"]).chmod(0o600)
        prepared = prepare_registry_filesystem(registry, self.uid, self.gid, "https://git.example.test")
        self.assertEqual(len(prepared), 2)
        for repository in (first, second):
            marker = Path(repository["worktree_root"]) / MANAGED_MARKER
            self.assertEqual(marker.read_text(encoding="utf-8"), MANAGED_MARKER_CONTENT)
            self.assertEqual(marker.stat().st_mode & 0o777, 0o600)

    @skipUnless(subprocess.run(["sh", "-c", "command -v docker >/dev/null && docker compose version >/dev/null 2>&1"], check=False).returncode == 0, "Docker Compose is unavailable")
    def test_rendered_override_is_accepted_by_docker_compose(self) -> None:
        registry = load_operator_registry(self.write_registry([
            self.repository("switchyard", "subculture-collective/switchyard"),
            self.repository("dsa-seats", "subculture-collective/dsa-seats"),
        ]))
        fingerprint = registry_fingerprint(registry)
        manifest_path = self.root / "runtime.json"
        override_path = self.root / "compose.json"
        write_atomic(manifest_path, canonical_json(render_runtime_manifest(registry, fingerprint)))
        write_atomic(override_path, canonical_json(render_compose_override(registry, manifest_path, fingerprint)))
        file_set_path = self.root / "file-set.json"
        base_compose = Path(__file__).resolve().parents[1] / "docker-compose.yml"
        write_atomic(file_set_path, canonical_json(render_compose_file_set(base_compose, override_path, manifest_path, fingerprint)))
        self.assertEqual(resolve(file_set_path, base_compose), (base_compose.resolve(), override_path.resolve()))
        environment = os.environ.copy()
        environment.update({"POSTGRES_PASSWORD": "compose-test-password", "OPENCODE_SERVER_PASSWORD": "legacy-compose-test-password"})
        result = subprocess.run(
            ["docker", "compose", "--env-file", "/dev/null", "-f", str(Path(__file__).resolve().parents[1] / "docker-compose.yml"), "-f", str(override_path), "config", "--format", "json"],
            env=environment, capture_output=True, text=True, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        rendered = json.loads(result.stdout)
        self.assertIn("open-pilot-opencode-dsa-seats", rendered["services"])
        self.assertEqual(rendered["services"]["open-pilot-verifier-switchyard"]["networks"], {"open-pilot-verification-switchyard": None})


if __name__ == "__main__":
    unittest.main()
