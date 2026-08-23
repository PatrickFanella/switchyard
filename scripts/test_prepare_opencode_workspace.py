import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from prepare_opencode_workspace import prepare
from validate_production_config import MANAGED_MARKER, WORKTREE_DIRECTORY


class PrepareOpenCodeWorkspaceTest(unittest.TestCase):
    def test_creates_documented_default_workspace(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = {
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            with patch("prepare_opencode_workspace.REPO_ROOT", root):
                workspace = prepare(env)

            self.assertEqual(workspace, root / "tmp/opencode-workspace")
            self.assertTrue(workspace.is_dir())
            self.assertTrue((workspace / ".git").is_dir())
            worktree_root = workspace / WORKTREE_DIRECTORY
            marker = worktree_root / MANAGED_MARKER
            self.assertTrue(worktree_root.is_dir())
            self.assertTrue(marker.is_file())
            self.assertEqual(marker.stat().st_mode & 0o777, 0o600)

    def test_creates_workspace_for_configured_owner(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory) / "workspace"
            workspace.mkdir()
            self.run_git("init", "--initial-branch=main", str(workspace))
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            if os.getuid() == 0 or os.getgid() == 0:
                self.skipTest("non-root ownership assertion requires a non-root test user")

            with patch("prepare_opencode_workspace.REPO_ROOT", Path(directory)):
                self.assertEqual(prepare(env), workspace)
            self.assertTrue((workspace / WORKTREE_DIRECTORY / MANAGED_MARKER).is_file())

    def test_refuses_missing_absolute_workspace(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            env = {
                "OPENCODE_WORKSPACE": str(Path(directory) / "missing"),
                "OPENCODE_RUNTIME_UID": str(os.getuid() or 1),
                "OPENCODE_RUNTIME_GID": str(os.getgid() or 1),
            }
            with self.assertRaisesRegex(ValueError, "must already exist"):
                prepare(env)

    def test_refuses_absolute_workspace_with_symlinked_parent(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            real_parent = Path(directory) / "real"
            real_parent.mkdir()
            workspace = real_parent / "workspace"
            workspace.mkdir()
            self.run_git("init", "--initial-branch=main", str(workspace))
            linked_parent = Path(directory) / "linked"
            linked_parent.symlink_to(real_parent, target_is_directory=True)
            env = {
                "OPENCODE_WORKSPACE": str(linked_parent / "workspace"),
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            with self.assertRaisesRegex(ValueError, "canonical"):
                prepare(env)

    def test_refuses_relative_workspace_escape(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repo"
            root.mkdir()
            env = {
                "OPENCODE_WORKSPACE": "../outside",
                "OPENCODE_RUNTIME_UID": str(os.getuid() or 1),
                "OPENCODE_RUNTIME_GID": str(os.getgid() or 1),
            }
            with patch("prepare_opencode_workspace.REPO_ROOT", root):
                with self.assertRaisesRegex(ValueError, "must remain inside"):
                    prepare(env)
            self.assertFalse((Path(directory) / "outside").exists())

    def test_refuses_owner_mismatch_without_chowning(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory) / "workspace"
            workspace.mkdir()
            original_owner = workspace.stat()
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_RUNTIME_UID": str(original_owner.st_uid + 1),
                "OPENCODE_RUNTIME_GID": str(original_owner.st_gid + 1),
            }

            with self.assertRaisesRegex(ValueError, "refusing to change ownership"):
                prepare(env)
            current_owner = workspace.stat()
            self.assertEqual(current_owner.st_uid, original_owner.st_uid)
            self.assertEqual(current_owner.st_gid, original_owner.st_gid)

    def test_validates_repo_owned_by_runtime_user_when_deployer_is_root(self) -> None:
        if os.getuid() != 0:
            self.skipTest("different deploy/runtime ownership requires root")
        runtime_uid = 65534
        runtime_gid = 65534
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workspace = root / "workspace"
            workspace.mkdir(mode=0o700)
            self.run_git("init", "--initial-branch=main", str(workspace))
            worktree_root = workspace / WORKTREE_DIRECTORY
            worktree_root.mkdir(mode=0o700)
            marker = worktree_root / MANAGED_MARKER
            marker.write_text("switchyard-managed-worktree-root\n", encoding="utf-8")
            marker.chmod(0o600)
            config = root / "config"
            data = root / "data"
            config.mkdir(mode=0o700)
            data.mkdir(mode=0o700)
            for path in (workspace, workspace / ".git", worktree_root, marker, config, data):
                os.chown(path, runtime_uid, runtime_gid)
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_CONFIG_DIR": str(config),
                "OPENCODE_DATA_DIR": str(data),
                "OPENCODE_RUNTIME_UID": str(runtime_uid),
                "OPENCODE_RUNTIME_GID": str(runtime_gid),
            }
            with patch("prepare_opencode_workspace.REPO_ROOT", root):
                self.assertEqual(prepare(env), workspace)

    def test_refuses_repository_ancestor(self) -> None:
        env = {
            "OPENCODE_WORKSPACE": str(Path(__file__).resolve().parents[2]),
            "OPENCODE_RUNTIME_UID": str(os.getuid() or 1),
            "OPENCODE_RUNTIME_GID": str(os.getgid() or 1),
        }
        with self.assertRaisesRegex(ValueError, "must not contain this repository"):
            prepare(env)

    def test_refuses_symlinked_managed_marker(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory) / "workspace"
            workspace.mkdir()
            self.run_git("init", "--initial-branch=main", str(workspace))
            root = workspace / WORKTREE_DIRECTORY
            root.mkdir()
            target = workspace / "target"
            target.write_text("switchyard-managed-worktree-root\n", encoding="utf-8")
            (root / MANAGED_MARKER).symlink_to(target)
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            with self.assertRaisesRegex(ValueError, "marker is invalid"):
                prepare(env)

    def test_refuses_nonempty_default_without_git_metadata(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workspace = root / "tmp/opencode-workspace"
            workspace.mkdir(parents=True)
            (workspace / "unexpected").write_text("do not mutate\n", encoding="utf-8")
            env = {
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            with patch("prepare_opencode_workspace.REPO_ROOT", root):
                with self.assertRaisesRegex(ValueError, "must be empty"):
                    prepare(env)
            self.assertFalse((workspace / ".git").exists())

    def test_refuses_absolute_non_git_workspace(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory) / "workspace"
            workspace.mkdir()
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            with self.assertRaisesRegex(ValueError, "valid Git top-level"):
                prepare(env)

    def test_refuses_git_file_metadata(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory) / "workspace"
            workspace.mkdir()
            (workspace / ".git").write_text("gitdir: /tmp/fake\n", encoding="utf-8")
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            with self.assertRaisesRegex(ValueError, "non-symlink directory"):
                prepare(env)

    def test_refuses_unsafe_local_git_config_without_exposing_value(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory) / "workspace"
            workspace.mkdir()
            self.run_git("init", "--initial-branch=main", str(workspace))
            marker = "credential-marker-must-not-print"
            self.run_git("-C", str(workspace), "config", "--local", "remote.origin.pushurl", marker)
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            with self.assertRaises(ValueError) as raised:
                prepare(env)
            self.assertIn("redirect or credential", str(raised.exception))
            self.assertNotIn(marker, str(raised.exception))

    def test_refuses_runtime_inaccessible_git_metadata(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory) / "workspace"
            workspace.mkdir()
            self.run_git("init", "--initial-branch=main", str(workspace))
            (workspace / ".git").chmod(0o500)
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            with self.assertRaisesRegex(ValueError, "read, write, and traverse"):
                prepare(env)

    def test_refuses_group_or_world_writable_trust_boundary(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workspace = root / "workspace"
            workspace.mkdir(mode=0o700)
            self.run_git("init", "--initial-branch=main", str(workspace))
            worktree_root = workspace / WORKTREE_DIRECTORY
            worktree_root.mkdir(mode=0o700)
            marker = worktree_root / MANAGED_MARKER
            marker.write_text("switchyard-managed-worktree-root\n", encoding="utf-8")
            marker.chmod(0o600)
            config = root / "config"
            data = root / "data"
            config.mkdir(mode=0o700)
            data.mkdir(mode=0o700)
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_CONFIG_DIR": str(config),
                "OPENCODE_DATA_DIR": str(data),
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            paths = {
                "workspace": workspace,
                "config": config,
                "data": data,
                "git": workspace / ".git",
                "worktree root": worktree_root,
                "marker": marker,
            }
            with patch("prepare_opencode_workspace.REPO_ROOT", root):
                for name, path in paths.items():
                    original = path.stat().st_mode & 0o777
                    for write_bit in (0o020, 0o002):
                        with self.subTest(path=name, write_bit=oct(write_bit)):
                            path.chmod(original | write_bit)
                            with self.assertRaisesRegex(ValueError, "writable|private"):
                                prepare(env)
                            path.chmod(original)

    def test_refuses_config_directory_with_symlinked_parent(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workspace = root / "workspace"
            workspace.mkdir()
            self.run_git("init", "--initial-branch=main", str(workspace))
            real = root / "real"
            real.mkdir()
            config = real / "config"
            config.mkdir(mode=0o700)
            link = root / "linked"
            link.symlink_to(real, target_is_directory=True)
            data = root / "data"
            data.mkdir(mode=0o700)
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_CONFIG_DIR": str(link / "config"),
                "OPENCODE_DATA_DIR": str(data),
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            with patch("prepare_opencode_workspace.REPO_ROOT", root):
                with self.assertRaisesRegex(ValueError, "canonical"):
                    prepare(env)

    def test_relative_config_does_not_create_through_symlinked_parent(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workspace = root / "workspace"
            workspace.mkdir()
            self.run_git("init", "--initial-branch=main", str(workspace))
            outside = root / "outside"
            outside.mkdir()
            (root / "linked").symlink_to(outside, target_is_directory=True)
            data = root / "data"
            data.mkdir(mode=0o700)
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_CONFIG_DIR": "linked/config",
                "OPENCODE_DATA_DIR": str(data),
                "OPENCODE_RUNTIME_UID": str(os.getuid()),
                "OPENCODE_RUNTIME_GID": str(os.getgid()),
            }
            with patch("prepare_opencode_workspace.REPO_ROOT", root):
                with self.assertRaisesRegex(ValueError, "symlink components"):
                    prepare(env)
            self.assertFalse((outside / "config").exists())

    def test_refuses_config_directory_owner_mismatch_without_chown(self) -> None:
        if os.getuid() == 0 or os.getgid() == 0:
            self.skipTest("non-root ownership assertion requires a non-root test user")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workspace = root / "workspace"
            workspace.mkdir()
            self.run_git("init", "--initial-branch=main", str(workspace))
            config = root / "config"
            data = root / "data"
            config.mkdir(mode=0o700)
            data.mkdir(mode=0o700)
            original = config.stat()
            env = {
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_CONFIG_DIR": str(config),
                "OPENCODE_DATA_DIR": str(data),
                "OPENCODE_RUNTIME_UID": str(os.getuid() + 1),
                "OPENCODE_RUNTIME_GID": str(os.getgid() + 1),
            }
            with patch("prepare_opencode_workspace.REPO_ROOT", root):
                with self.assertRaisesRegex(ValueError, "ownership"):
                    prepare(env)
            self.assertEqual((config.stat().st_uid, config.stat().st_gid), (original.st_uid, original.st_gid))

    @staticmethod
    def run_git(*args: str) -> None:
        import subprocess

        subprocess.run(["git", *args], check=True, capture_output=True, text=True)


if __name__ == "__main__":
    unittest.main()
