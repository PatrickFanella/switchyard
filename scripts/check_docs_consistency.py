#!/usr/bin/env python3
import json
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.parse import urlparse


ROOT = Path(__file__).resolve().parent.parent
INLINE_LINK = re.compile(r"!?\[[^]]*\]\(\s*(<[^>]+>|[^)\s]+)(?:\s+[^)]*)?\)")
REFERENCE_LINK = re.compile(r"!?\[([^]]*)\]\[([^]]*)\]")
REFERENCE_DEFINITION = re.compile(r"^[ \t]{0,3}\[([^]]+)\]:[ \t]*(<[^>]+>|\S+)", re.MULTILINE)
STATUS_ROW = re.compile(r"^\| ([^|]+?) \| ([^|]+?) \|", re.MULTILINE)
SEMVER_TAG = re.compile(r"v?(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?")
FULL_SHA = re.compile(r"[0-9a-f]{40}")
CAPABILITY_STATES = {
    "implemented",
    "test-verified",
    "qualified-with-exceptions",
    "historical-evidence",
}


class ContractError(ValueError):
    pass


def read_text(path: Path, label: str) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeError):
        raise ContractError(f"{label}: could not read required file") from None


def read_json(path: Path, label: str) -> dict:
    try:
        value = json.loads(read_text(path, label))
    except json.JSONDecodeError:
        raise ContractError(f"{label}: invalid JSON") from None
    if not isinstance(value, dict):
        raise ContractError(f"{label}: root must be an object")
    return value


def contained_path(root: Path, relative: str, label: str) -> Path:
    if not isinstance(relative, str) or not relative or Path(relative).is_absolute():
        raise ContractError(f"{label}: path must be a non-empty repository-relative string")
    canonical_root = root.resolve()
    try:
        candidate = (canonical_root / relative).resolve(strict=True)
        candidate.relative_to(canonical_root)
    except (OSError, RuntimeError, ValueError):
        raise ContractError(f"{label}: path is missing or escapes the repository") from None
    return candidate


def tracked_files(root: Path) -> set[str]:
    try:
        result = subprocess.run(
            ["git", "ls-files", "-z"], cwd=root, capture_output=True, check=False
        )
    except OSError:
        raise ContractError("capability evidence check could not list tracked files") from None
    if result.returncode != 0:
        raise ContractError("capability evidence check could not list tracked files")
    try:
        return {item.decode("utf-8") for item in result.stdout.split(b"\0") if item}
    except UnicodeDecodeError:
        raise ContractError("capability evidence check returned invalid paths") from None


def dotenv(path: Path) -> dict[str, str]:
    values = {}
    for line in read_text(path, ".env.example").splitlines():
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            name, value = line.split("=", 1)
            values[name] = value
    return values


def validate_capabilities(root: Path) -> None:
    label = "docs/capabilities-v1.json"
    contract = read_json(root / label, label)
    if contract.get("version") != 1:
        raise ContractError("docs/capabilities-v1.json: unsupported version")
    states = contract.get("states", [])
    if not isinstance(states, list) or not all(isinstance(state, str) for state in states):
        raise ContractError(f"{label}: states must be a string array")
    if set(states) != CAPABILITY_STATES or len(states) != len(CAPABILITY_STATES):
        raise ContractError("docs/capabilities-v1.json: states do not match the fixed capability enum")
    status = read_text(root / "docs/status.md", "docs/status.md")
    rows = {
        name.strip(): state.strip()
        for name, state in STATUS_ROW.findall(status)
        if name not in {"Capability", "---"}
    }
    expected = {}
    capabilities = contract.get("capabilities", [])
    if not isinstance(capabilities, list):
        raise ContractError(f"{label}: capabilities must be an array")
    tracked = tracked_files(root)
    for capability in capabilities:
        if not isinstance(capability, dict):
            raise ContractError(f"{label}: each capability must be an object")
        name = capability.get("name", "")
        state = capability.get("state", "")
        if not isinstance(name, str) or not isinstance(state, str):
            raise ContractError(f"{label}: capability name and state must be strings")
        if not name or name in expected:
            raise ContractError("docs/capabilities-v1.json: capability names must be non-empty and unique")
        if state not in states:
            raise ContractError(f"docs/capabilities-v1.json: {name}: state is outside the fixed enum")
        implementation = capability.get("implementation", [])
        tests = capability.get("tests", [])
        if not isinstance(implementation, list) or not isinstance(tests, list):
            raise ContractError(f"{label}: {name}: evidence must be string arrays")
        if not all(isinstance(path, str) for path in implementation + tests):
            raise ContractError(f"{label}: {name}: evidence must be string arrays")
        if not implementation:
            raise ContractError(f"docs/capabilities-v1.json: {name}: implementation evidence is required")
        if state == "test-verified" and not tests:
            raise ContractError(f"docs/capabilities-v1.json: {name}: test-verified requires test evidence")
        if state == "test-verified" and not all(is_test_evidence(path) for path in tests):
            raise ContractError(f"docs/capabilities-v1.json: {name}: test evidence must name test files")
        for evidence in implementation + tests:
            candidate = contained_path(root, evidence, f"{label}: {name}: evidence")
            if not candidate.is_file():
                raise ContractError(f"{label}: {name}: evidence must name a file")
            if evidence not in tracked:
                raise ContractError(f"{label}: {name}: evidence must be tracked by git")
        expected[name] = state
    if rows != expected:
        missing = sorted(set(expected) - set(rows))
        extra = sorted(set(rows) - set(expected))
        mismatched = sorted(name for name in set(rows) & set(expected) if rows[name] != expected[name])
        raise ContractError(f"docs/status.md: capability contract mismatch; missing={missing} extra={extra} states={mismatched}")


def validate_release_pointer(root: Path) -> None:
    status = read_text(root / "docs/status.md", "docs/status.md")
    tags = re.findall(r"^- Current deployed release: `([^`]*)`$", status, re.MULTILINE)
    shas = re.findall(r"^- Current deployed revision: `([^`]*)`$", status, re.MULTILINE)
    if len(tags) != 1 or len(shas) != 1 or not SEMVER_TAG.fullmatch(tags[0]) or not FULL_SHA.fullmatch(shas[0]):
        raise ContractError("docs/status.md: exactly one semver release and full lowercase revision are required")
    tag, sha = tags[0], shas[0]
    relative_record = f"docs/releases/{tag}-qualification.md"
    record_path = contained_path(root, relative_record, "docs/status.md: qualification record")
    if not record_path.is_file():
        raise ContractError("docs/status.md: qualification record must be a file")
    record = read_text(record_path, relative_record)
    releases = re.findall(r"^- Release: `([^`]*)`$", record, re.MULTILINE)
    revisions = re.findall(r"^- Revision: `([^`]*)`$", record, re.MULTILINE)
    if releases != [tag] or revisions != [sha]:
        raise ContractError("docs/status.md: deployed pointer does not match its qualification record")


def tracked_markdown(root: Path) -> list[Path]:
    result = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "*.md"],
        cwd=root,
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        raise ContractError("Markdown link check could not list tracked files")
    return [root / line for line in result.stdout.splitlines() if line]


def is_test_evidence(path: str) -> bool:
    name = Path(path).name
    return (
        name.endswith("_test.go")
        or name.startswith("test_") and name.endswith(".py")
        or name.endswith((".test.ts", ".test.tsx", ".test.js", ".test.jsx"))
        or name.startswith("test-") and name.endswith(".sh")
    )


def validate_markdown_links(root: Path, files: list[Path] | None = None) -> None:
    for source in files if files is not None else tracked_markdown(root):
        try:
            shown = source.relative_to(root)
        except ValueError:
            raise ContractError("Markdown source path escapes the repository") from None
        text = read_text(source, str(shown))
        text = re.sub(r"```.*?```", "", text, flags=re.DOTALL)
        definitions = {
            " ".join(name.lower().split()): target
            for name, target in REFERENCE_DEFINITION.findall(text)
        }
        for label, reference in REFERENCE_LINK.findall(text):
            key = " ".join((reference or label).lower().split())
            if key not in definitions:
                raise ContractError(f"{shown}: Markdown reference is undefined")
        targets = INLINE_LINK.findall(text) + list(definitions.values())
        for raw_target in targets:
            target = raw_target.strip().strip("<>")
            if not target or target.startswith(("#", "http://", "https://", "mailto:")):
                continue
            relative = target.split("#", 1)[0].split("?", 1)[0]
            if relative:
                resolved = (source.parent / relative).resolve()
                try:
                    resolved.relative_to(root.resolve())
                except ValueError:
                    raise ContractError(f"{shown}: relative Markdown link escapes the repository: {relative}") from None
                if not resolved.exists():
                    raise ContractError(f"{shown}: relative Markdown link does not exist: {relative}")


def validate_codemap(root: Path) -> None:
    codemap = read_text(root / "codemap.md", "codemap.md")
    if codemap.count("## System Entry Points") != 1 or codemap.count("## Deliberately Unmapped") != 1:
        raise ContractError("codemap.md: required sections must appear exactly once")
    entrypoint_section = codemap.split("## System Entry Points", 1)[1].split("##", 1)[0]
    for path in re.findall(r"`([^`]+)`", entrypoint_section):
        contained_path(root, path, "codemap.md: key entrypoint")
    deliberately_unmapped = codemap.split("## Deliberately Unmapped", 1)[-1]
    if "`docs/`, tests" not in deliberately_unmapped:
        raise ContractError("codemap.md: deliberate docs/test cartography exclusion must remain documented")


def validate_current_docs(root: Path) -> None:
    required = (
        "docs/status.md",
        "docs/roadmap.md",
        "docs/adr/0003-native-switchyard-modules.md",
        "docs/runbooks/release-and-rollback.md",
        "docs/runbooks/postgres-backup-and-restore.md",
        "docs/runbooks/incident-response.md",
        "docs/runbooks/native-model-broker-cutover.md",
        "docs/reference/client-compatibility.md",
        "docs/openapi.yaml",
        "docs/openapi-model-ingress.yaml",
    )
    for path in required:
        if not (root / path).is_file():
            raise ContractError(f"required current document does not exist: {path}")
    status = (root / "docs/status.md").read_text(encoding="utf-8")
    for command in ("make verify-production", "make production-smoke"):
        if command not in status:
            raise ContractError(f"docs/status.md: required release command is missing: {command}")
    for plan in (root / "docs/superpowers/plans").glob("*.md"):
        opening = "\n".join(plan.read_text(encoding="utf-8").splitlines()[:8]).lower()
        if "historical" not in opening and "completed plan" not in opening:
            raise ContractError(f"{plan.relative_to(root)}: plan must declare historical/completed status near its title")


def render_compose(root: Path) -> dict:
    with tempfile.TemporaryDirectory() as directory:
        base = Path(directory)
        for name in ("workspace/.git", "workspace/.open-pilot-worktrees", "config", "data", "metrics"):
            (base / name).mkdir(parents=True)
        env = {
            "PATH": __import__("os").environ.get("PATH", ""),
            "HOME": __import__("os").environ.get("HOME", "/tmp"),
            "POSTGRES_PASSWORD": "docs-contract-placeholder",
            "OPENCODE_WORKSPACE": str(base / "workspace"),
            "OPENCODE_CONFIG_DIR": str(base / "config"),
            "OPENCODE_DATA_DIR": str(base / "data"),
            "SWITCHYARD_BACKUP_METRICS_DIR": str(base / "metrics"),
        }
        result = subprocess.run(
            ["docker", "compose", "--env-file", "/dev/null", "config", "--format", "json"],
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
    if result.returncode != 0:
        raise ContractError("docker compose --env-file /dev/null config failed")
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise ContractError("docker compose config did not return JSON") from error


def validate_defaults(root: Path, compose: dict | None = None) -> None:
    contract = read_json(root / "backend/internal/runtimeconfig/defaults-v1.json", "runtime defaults contract")
    if contract.get("version") != 1:
        raise ContractError("runtime defaults contract: unsupported version")
    example = dotenv(root / ".env.example")
    rendered = compose if compose is not None else render_compose(root)
    try:
        api = rendered["services"]["api"].get("environment", {})
        opencode = rendered["services"]["opencode"].get("environment", {})
        defaults = contract.get("defaults", {})
        listeners = contract.get("listeners", {})
        if not all(isinstance(value, dict) for value in (api, opencode, defaults, listeners)):
            raise TypeError
    except (KeyError, TypeError, AttributeError):
        raise ContractError("runtime defaults contract: malformed compose or defaults structure") from None
    for name, values in defaults.items():
        if not isinstance(name, str) or not isinstance(values, dict):
            raise ContractError("runtime defaults contract: malformed default entry")
        if example.get(name) != values.get("example"):
            raise ContractError(f".env.example: critical default mismatch: {name}")
        environment = opencode if name in {
            "OPENCODE_SESSION_CREATE_TIMEOUT_MS", "OPENCODE_MESSAGE_TIMEOUT_MS", "OPENCODE_SECURITY_MODE"
        } else api
        actual = environment.get(name, "")
        if str(actual) != str(values.get("compose", "")):
            raise ContractError(f"docker-compose.yml: rendered critical default mismatch: {name}")
        if values.get("runtime") != values.get("compose") and not values.get("deployment_override"):
            raise ContractError(f"runtime defaults contract: undocumented deployment override: {name}")
        if values.get("runtime") != values.get("example") and not (values.get("deployment_override") or values.get("secret")):
            raise ContractError(f"runtime defaults contract: undocumented example override: {name}")
    ports = rendered["services"]["api"].get("ports", [])
    if not isinstance(ports, list) or not all(isinstance(port, dict) for port in ports):
        raise ContractError("docker-compose.yml: rendered ports are malformed")
    for listener, values in listeners.items():
        try:
            bind = example.get(values["bind_env"])
            published = example.get(values["port_env"])
            match = next((port for port in ports if port.get("target") == values["target"]), None)
        except (KeyError, TypeError):
            raise ContractError("runtime defaults contract: malformed listener entry") from None
        if match is None or match.get("host_ip") != bind or str(match.get("published")) != published:
            raise ContractError(f"docker-compose.yml: rendered {listener} listener bind/port mismatch")
    try:
        api_url = urlparse(example.get("SWITCHYARD_API_URL", ""))
        control = listeners["control"]
        expected_host = example.get(control["bind_env"])
        expected_port = example.get(control["port_env"])
        actual_port = api_url.port
    except (KeyError, TypeError, ValueError):
        raise ContractError(".env.example: control listener URL or port is malformed") from None
    if api_url.scheme not in {"http", "https"} or api_url.hostname != expected_host or str(actual_port) != expected_port:
        raise ContractError(".env.example: SWITCHYARD_API_URL does not match the control listener bind/port")


def main() -> None:
    checks = (
        validate_capabilities,
        validate_release_pointer,
        validate_markdown_links,
        validate_codemap,
        validate_current_docs,
        validate_defaults,
    )
    for check in checks:
        check(ROOT)
    print("Documentation consistency checks passed")


if __name__ == "__main__":
    try:
        main()
    except ContractError as error:
        raise SystemExit(f"docs contract failed: {error}") from None
    except (KeyError, TypeError, AttributeError, ValueError, OSError, UnicodeError, json.JSONDecodeError):
        error = ContractError("malformed documentation contract")
        raise SystemExit(f"docs contract failed: {error}") from None
