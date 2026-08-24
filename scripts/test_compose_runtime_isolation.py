import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


REPO_ROOT = Path(__file__).resolve().parent.parent
WORKSPACE_TARGET = "/workspace"
WORKTREE_ROOT = "/workspace/.open-pilot-worktrees"


class ComposeRuntimeIsolationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory) / "workspace"
            metrics = Path(directory) / "metrics"
            workspace.mkdir()
            git_source = workspace / ".git"
            git_source.mkdir()
            worktree_source = workspace / ".open-pilot-worktrees"
            worktree_source.mkdir()
            metrics.mkdir()
            config_dir = Path(directory) / "config"
            data_dir = Path(directory) / "data"
            config_dir.mkdir()
            data_dir.mkdir()
            env = {
                "PATH": os.environ["PATH"],
                "HOME": os.environ.get("HOME", "/tmp"),
                "POSTGRES_PASSWORD": "compose-contract-test-only",
                "OPENCODE_SERVER_PASSWORD": "compose-contract-secret-marker",
                "OPENCODE_WORKSPACE": str(workspace),
                "OPENCODE_CONFIG_DIR": str(config_dir),
                "OPENCODE_DATA_DIR": str(data_dir),
                "SWITCHYARD_BACKUP_METRICS_DIR": str(metrics),
                "OPENCODE_RUNTIME_UID": "12345",
                "OPENCODE_RUNTIME_GID": "12346",
                "OPENCODE_SECURITY_MODE": "strict",
                "OPEN_PILOT_REPO_PATH": WORKSPACE_TARGET,
                "OPEN_PILOT_WORKTREE_ROOT": WORKTREE_ROOT,
                "SWITCHYARD_REVERSE_PROXY_TOKEN": "compose-proxy-token-at-least-32-bytes",
            }
            result = subprocess.run(
                [
                    "docker",
                    "compose",
                    "--env-file",
                    "/dev/null",
                    "config",
                    "--format",
                    "json",
                ],
                cwd=REPO_ROOT,
                env=env,
                capture_output=True,
                text=True,
                check=False,
            )
            if result.returncode != 0:
                raise AssertionError("docker compose config failed")
            try:
                cls.config = json.loads(result.stdout)
            except json.JSONDecodeError as error:
                raise AssertionError("docker compose config did not return JSON") from error
            cls.workspace = str(workspace)
            cls.worktree_source = str(worktree_source)
            cls.git_source = str(git_source)
            cls.metrics = str(metrics)
            cls.config_dir = str(config_dir)
            cls.data_dir = str(data_dir)

    def service(self, name: str) -> dict:
        try:
            return self.config["services"][name]
        except KeyError as error:
            self.fail(f"Compose service contract missing: {name}")
            raise error

    def assert_hardened(self, name: str) -> None:
        service = self.service(name)
        self.assertIs(service.get("read_only"), True, f"{name} rootfs must be read-only")
        self.assertEqual(service.get("cap_drop"), ["ALL"], f"{name} must drop all capabilities")
        self.assertIn(
            "no-new-privileges:true",
            service.get("security_opt", []),
            f"{name} must set no-new-privileges",
        )

    def volume(self, name: str, target: str) -> dict:
        for volume in self.service(name).get("volumes", []):
            if volume.get("target") == target:
                return volume
        self.fail(f"{name} mount contract missing target {target}")

    def test_api_runtime_and_mount_contract(self) -> None:
        self.assert_hardened("api")
        api = self.service("api")
        self.assertEqual(api.get("user"), "12345:12346")
        self.assertEqual(api.get("build", {}).get("target"), "api")
        self.assertEqual(api["environment"].get("GIT_TERMINAL_PROMPT"), "0")
        self.assertEqual(api["environment"].get("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE"), "false")
        self.assertEqual(api["environment"].get("SWITCHYARD_REVERSE_PROXY_TOKEN"), "compose-proxy-token-at-least-32-bytes")
        self.assertIn("/tmp:rw,exec,nosuid,size=256m", api.get("tmpfs", []))
        self.assertIn("/run:rw,noexec,nosuid,size=16m", api.get("tmpfs", []))
        volumes = api.get("volumes", [])
        self.assertEqual(len(volumes), 2, "api must expose only workspace and metrics mounts")
        workspace = self.volume("api", WORKSPACE_TARGET)
        self.assertEqual(workspace.get("source"), self.workspace)
        self.assertIsNot(workspace.get("read_only", False), True)
        self.assertIs(workspace.get("bind", {}).get("create_host_path"), False)
        metrics = self.volume("api", "/var/lib/switchyard-metrics")
        self.assertEqual(metrics.get("source"), self.metrics)
        self.assertIs(metrics.get("read_only"), True)
        self.assertIs(metrics.get("bind", {}).get("create_host_path"), False)

    def test_opencode_and_verifier_keep_existing_isolation(self) -> None:
        self.assert_hardened("opencode")
        opencode = self.service("opencode")
        self.assertEqual(opencode.get("user"), "0:0")
        self.assertEqual(set(opencode.get("cap_add", [])), {"CHOWN", "SETGID", "SETUID"})
        self.assertEqual(opencode["environment"].get("OPENCODE_SECURITY_MODE"), "strict")
        self.assertEqual(opencode["environment"].get("OPENCODE_DB"), ":memory:")
        self.assertEqual(opencode["environment"].get("OPENCODE_DISABLE_PROJECT_CONFIG"), "true")
        self.assertEqual(opencode["environment"].get("OPENCODE_DISABLE_CLAUDE_CODE_PROMPT"), "true")
        self.assertEqual(opencode["environment"].get("OPENCODE_RUNTIME_UID"), "12345")
        self.assertEqual(opencode["environment"].get("OPENCODE_RUNTIME_GID"), "12346")
        self.assertEqual(opencode["environment"].get("GIT_OPTIONAL_LOCKS"), "0")
        self.assertEqual(opencode["environment"].get("GIT_TERMINAL_PROMPT"), "0")
        self.assertIn("/tmp:rw,noexec,nosuid,size=256m", opencode.get("tmpfs", []))
        opencode_worktrees = self.volume("opencode", WORKTREE_ROOT)
        self.assertEqual(opencode_worktrees.get("source"), self.worktree_source)
        self.assertIsNot(opencode_worktrees.get("read_only", False), True)
        self.assertIs(opencode_worktrees.get("bind", {}).get("create_host_path"), False)
        self.assertIsNone(next((v for v in opencode.get("volumes", []) if v.get("target") == WORKSPACE_TARGET), None))
        opencode_git = self.volume("opencode", "/workspace/.git")
        self.assertEqual(opencode_git.get("source"), self.git_source)
        self.assertIs(opencode_git.get("read_only"), True)
        self.assertIs(opencode_git.get("bind", {}).get("create_host_path"), False)
        self.assertEqual(opencode["environment"].get("OPEN_PILOT_WORKTREE_ROOT"), WORKTREE_ROOT)
        for target in (
            "/home/opencode/.config/opencode",
            "/home/opencode/.local/share/opencode",
        ):
            self.assertIs(
                self.volume("opencode", target).get("bind", {}).get("create_host_path"),
                False,
            )
        self.assertEqual(self.volume("opencode", "/home/opencode/.config/opencode").get("source"), self.config_dir)
        self.assertEqual(self.volume("opencode", "/home/opencode/.local/share/opencode").get("source"), self.data_dir)

        entrypoint = (REPO_ROOT / "services/opencode/entrypoint.sh").read_text(encoding="utf-8")
        self.assertNotIn("chown -R", entrypoint)
        self.assertIn("refusing recursive chown", entrypoint)

        self.assert_hardened("verifier")
        verifier = self.service("verifier")
        self.assertEqual(verifier.get("user"), "12345:12346")
        self.assertEqual(verifier.get("build", {}).get("target"), "verifier")
        self.assertEqual(set(verifier.get("networks", {})), {"verification"})
        self.assertIn("/tmp:rw,exec,nosuid,size=1g,mode=1777", verifier.get("tmpfs", []))
        self.assertIn("/cache:rw,exec,nosuid,size=512m,mode=1777", verifier.get("tmpfs", []))
        self.assertEqual(verifier.get("pids_limit"), 128)
        self.assertEqual(verifier.get("mem_limit"), "2147483648")
        self.assertEqual(verifier.get("cpus"), 2.0)
        self.assertEqual(verifier["environment"].get("SWITCHYARD_VERIFIER_CONCURRENCY"), "1")
        self.assertEqual(verifier["environment"].get("SWITCHYARD_VERIFIER_OUTPUT_BYTES"), "1048576")
        self.assertEqual(verifier["environment"].get("SWITCHYARD_VERIFIER_SNAPSHOT_MAX_FILES"), "10000")
        self.assertEqual(verifier["environment"].get("SWITCHYARD_VERIFIER_SNAPSHOT_MAX_BYTES"), "268435456")
        self.assertEqual(verifier["environment"].get("GIT_OPTIONAL_LOCKS"), "0")
        self.assertEqual(verifier["environment"].get("GIT_TERMINAL_PROMPT"), "0")
        self.assertEqual(verifier["environment"].get("OPEN_PILOT_GIT_COMMON_DIR"), "/workspace/.git")
        for name in ("GOCACHE", "GOMODCACHE", "GOTMPDIR", "CARGO_HOME", "PNPM_HOME", "npm_config_cache", "XDG_CACHE_HOME", "TMPDIR"):
            value = verifier["environment"].get(name, "")
            self.assertTrue(value == "/cache" or value.startswith("/cache/"), name)
        self.assertEqual(len(verifier.get("volumes", [])), 2)
        verifier_git = self.volume("verifier", "/workspace/.git")
        self.assertEqual(verifier_git.get("source"), self.git_source)
        self.assertIs(verifier_git.get("read_only"), True)
        self.assertIs(verifier_git.get("bind", {}).get("create_host_path"), False)
        verifier_worktrees = self.volume("verifier", WORKTREE_ROOT)
        self.assertEqual(verifier_worktrees.get("source"), self.worktree_source)
        self.assertIs(verifier_worktrees.get("read_only"), True)
        self.assertIs(verifier_worktrees.get("bind", {}).get("create_host_path"), False)

    def test_services_share_only_the_dedicated_workspace_contract(self) -> None:
        api_workspace = self.volume("api", WORKSPACE_TARGET)
        self.assertEqual(api_workspace.get("source"), self.workspace)
        for name in ("opencode", "verifier"):
            with self.subTest(service=name):
                worktrees = self.volume(name, WORKTREE_ROOT)
                self.assertEqual(worktrees.get("type"), "bind")
                self.assertEqual(worktrees.get("source"), self.worktree_source)
                self.assertNotEqual(worktrees.get("source"), str(REPO_ROOT))
                self.assertIsNone(
                    next((volume for volume in self.service(name).get("volumes", []) if volume.get("source") == self.workspace), None),
                    f"{name} must not mount the source checkout",
                )
                workspace_targets = {
                    volume.get("target")
                    for volume in self.service(name).get("volumes", [])
                    if volume.get("target", "").startswith("/workspace")
                }
                self.assertEqual(workspace_targets, {"/workspace/.git", WORKTREE_ROOT})

        api_environment = self.service("api")["environment"]
        verifier_environment = self.service("verifier")["environment"]
        self.assertEqual(api_environment.get("OPEN_PILOT_REPO_PATH"), WORKSPACE_TARGET)
        self.assertEqual(api_environment.get("OPEN_PILOT_WORKTREE_ROOT"), WORKTREE_ROOT)
        self.assertEqual(verifier_environment.get("OPEN_PILOT_WORKTREE_ROOT"), WORKTREE_ROOT)

    def test_default_network_pins_bridge_and_web_address(self) -> None:
        network = self.config["networks"]["default"]
        self.assertEqual(network["driver_opts"], {"com.docker.network.bridge.name": "switchyard0"})
        self.assertEqual(network["ipam"]["config"], [{"subnet": "172.31.250.0/24", "gateway": "172.31.250.1"}])
        self.assertEqual(self.service("api")["networks"]["default"]["ipv4_address"], "172.31.250.4")
        self.assertIn("verification", self.service("api")["networks"])
        self.assertEqual(self.service("web")["networks"]["default"]["ipv4_address"], "172.31.250.10")
        self.assertEqual(self.service("web")["environment"]["SWITCHYARD_WEB_TRUSTED_PROXY_CIDR"], "100.77.128.80/32")

    def test_all_bind_mounts_disable_implicit_host_path_creation(self) -> None:
        for name, service in self.config["services"].items():
            for volume in service.get("volumes", []):
                if volume.get("type") == "bind":
                    with self.subTest(service=name, target=volume.get("target")):
                        self.assertIs(volume.get("bind", {}).get("create_host_path"), False)

    def test_docker_up_prepares_bind_sources(self) -> None:
        makefile = (REPO_ROOT / "Makefile").read_text(encoding="utf-8")
        self.assertIn("docker-up: workspace-init ##", makefile)

    def test_backend_image_keeps_verifier_toolchain_out_of_api_target(self) -> None:
        dockerfile = (REPO_ROOT / "backend/Dockerfile").read_text(encoding="utf-8")
        self.assertIn("FROM runtime AS verifier", dockerfile)
        self.assertIn("FROM runtime AS api", dockerfile)
        self.assertLess(dockerfile.index("FROM runtime AS verifier"), dockerfile.index("FROM runtime AS api"))
        verifier_section, api_section = dockerfile.split("FROM runtime AS verifier", 1)[1].split("FROM runtime AS api", 1)
        for value in ("make=4.4.1-r3", "cargo=1.87.0-r1", "pnpm@10.33.0", "/usr/local/go"):
            self.assertIn(value, verifier_section)
            self.assertNotIn(value, api_section)

    def test_web_proxy_overwrites_forwarding_headers_and_forwards_hop_token(self) -> None:
        nginx = (REPO_ROOT / "web/nginx.conf").read_text(encoding="utf-8")
        api_location = nginx.split("location /api/ {", 1)[1].split("}", 1)[0]
        health_location = nginx.split("location = /healthz {", 1)[1].split("}", 1)[0]
        self.assertIn("proxy_set_header X-Forwarded-For $http_x_forwarded_for;", api_location)
        self.assertIn('proxy_set_header X-Forwarded-User "";', api_location)
        self.assertIn("proxy_set_header Remote-User $http_remote_user;", api_location)
        self.assertIn("proxy_set_header X-Switchyard-Proxy-Token $http_x_switchyard_proxy_token;", api_location)
        self.assertIn("proxy_set_header X-Forwarded-For $http_x_forwarded_for;", health_location)
        self.assertIn('proxy_set_header X-Forwarded-User "";', health_location)
        self.assertIn('proxy_set_header Remote-User "";', health_location)
        self.assertIn('proxy_set_header X-Switchyard-Proxy-Token "";', health_location)
        self.assertNotIn("$proxy_add_x_forwarded_for", nginx)


if __name__ == "__main__":
    unittest.main()
