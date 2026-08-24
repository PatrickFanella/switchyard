#!/usr/bin/env python3
"""Render the host nftables gate for the production web listener."""

from __future__ import annotations

import argparse
import ipaddress
import os
import re
import stat
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Mapping


CGNAT = ipaddress.IPv4Network("100.64.0.0/10")
PRIVATE_NETWORKS = tuple(
    ipaddress.IPv4Network(value) for value in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")
)
ENV_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\Z")
MAX_ENV_BYTES = 1024 * 1024
FIELDS = {
    "destination": "SWITCHYARD_WEB_BIND_HOST",
    "proxy": "SWITCHYARD_TAILSCALE_PROXY_IPV4",
    "port": "WEB_PORT",
    "subnet": "SWITCHYARD_DOCKER_BRIDGE_SUBNET",
    "gateway": "SWITCHYARD_DOCKER_BRIDGE_GATEWAY",
    "bridge_name": "SWITCHYARD_DOCKER_BRIDGE_NAME",
    "web_container": "SWITCHYARD_WEB_CONTAINER_IPV4",
}
LINUX_INTERFACE_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,14}\Z")


class GateError(Exception):
    """A sanitized input or publication error."""


@dataclass(frozen=True)
class GateConfig:
    destination: ipaddress.IPv4Address
    proxy: ipaddress.IPv4Address
    port: int
    subnet: ipaddress.IPv4Network
    gateway: ipaddress.IPv4Address
    bridge_name: str
    web_container: ipaddress.IPv4Address


def read_dotenv(path: Path) -> dict[str, str]:
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        fd = os.open(path, flags)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_ENV_BYTES:
                raise GateError("invalid env file")
            raw = os.read(fd, MAX_ENV_BYTES + 1)
        finally:
            os.close(fd)
        if len(raw) > MAX_ENV_BYTES:
            raise GateError("invalid env file")
        text = raw.decode("utf-8")
    except GateError:
        raise
    except (OSError, UnicodeError) as exc:
        raise GateError("invalid env file") from exc

    values: dict[str, str] = {}
    for raw_line in text.splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].lstrip()
        if "=" not in line:
            raise GateError("invalid env file")
        name, raw_value = line.split("=", 1)
        name = name.strip()
        if not ENV_NAME.fullmatch(name) or name in values:
            raise GateError("invalid env file")
        value = raw_value.strip()
        if value.startswith(("'", '"')):
            quote = value[0]
            closing = value.find(quote, 1)
            if closing < 0:
                raise GateError("invalid env file")
            trailing = value[closing + 1 :].strip()
            if trailing and not trailing.startswith("#"):
                raise GateError("invalid env file")
            value = value[1:closing]
        else:
            value = re.split(r"\s+#", value, maxsplit=1)[0].rstrip()
        if "\x00" in value:
            raise GateError("invalid env file")
        values[name] = value
    return values


def _required(cli_value: str | None, values: Mapping[str, str], field: str) -> str:
    if cli_value is not None:
        return cli_value
    name = FIELDS[field]
    if name not in values or values[name] == "":
        raise GateError(f"missing {name}")
    return values[name]


def _ipv4(raw: str, name: str) -> ipaddress.IPv4Address:
    try:
        value = ipaddress.ip_address(raw)
    except ValueError as exc:
        raise GateError(f"invalid {name}") from exc
    if not isinstance(value, ipaddress.IPv4Address):
        raise GateError(f"invalid {name}")
    return value


def config_from_inputs(args: argparse.Namespace, values: Mapping[str, str]) -> GateConfig:
    destination = _ipv4(_required(args.destination, values, "destination"), FIELDS["destination"])
    proxy = _ipv4(_required(args.proxy, values, "proxy"), FIELDS["proxy"])
    if destination not in CGNAT:
        raise GateError(f"invalid {FIELDS['destination']}")
    if proxy not in CGNAT:
        raise GateError(f"invalid {FIELDS['proxy']}")
    if destination == proxy:
        raise GateError("Tailscale peer addresses must be distinct")

    raw_port = _required(args.port, values, "port")
    if not raw_port.isascii() or not raw_port.isdecimal():
        raise GateError(f"invalid {FIELDS['port']}")
    port = int(raw_port)
    if not 1 <= port <= 65535:
        raise GateError(f"invalid {FIELDS['port']}")

    raw_subnet = _required(args.subnet, values, "subnet")
    try:
        subnet = ipaddress.ip_network(raw_subnet, strict=True)
    except ValueError as exc:
        raise GateError(f"invalid {FIELDS['subnet']}") from exc
    if (
        not isinstance(subnet, ipaddress.IPv4Network)
        or subnet.prefixlen < 24
        or not any(subnet.subnet_of(private) for private in PRIVATE_NETWORKS)
    ):
        raise GateError(f"invalid {FIELDS['subnet']}")

    gateway = _ipv4(_required(args.gateway, values, "gateway"), FIELDS["gateway"])
    if gateway not in subnet or gateway in {subnet.network_address, subnet.broadcast_address}:
        raise GateError(f"invalid {FIELDS['gateway']}")
    bridge_name = _required(args.bridge_name, values, "bridge_name")
    if bridge_name in {".", ".."} or LINUX_INTERFACE_NAME.fullmatch(bridge_name) is None:
        raise GateError(f"invalid {FIELDS['bridge_name']}")
    web_container = _ipv4(_required(args.web_container, values, "web_container"), FIELDS["web_container"])
    if web_container not in subnet or web_container in {subnet.network_address, subnet.broadcast_address, gateway}:
        raise GateError(f"invalid {FIELDS['web_container']}")
    return GateConfig(destination, proxy, port, subnet, gateway, bridge_name, web_container)


def render(config: GateConfig) -> str:
    return f'''#!/usr/sbin/nft -f
# Generated by render_tailscale_web_gate.py. Do not edit.
# Docker bridge: {config.subnet}; gateway: {config.gateway}
destroy table inet switchyard_web_gate

table inet switchyard_web_gate {{
    chain prerouting {{
        type filter hook prerouting priority dstnat - 1; policy accept;
        iifname "tailscale0" ip saddr {config.proxy} ip daddr {config.destination} tcp dport {config.port} counter accept comment "allow Tailscale proxy to web"
        ip daddr {config.destination} tcp dport {config.port} counter drop comment "drop other ingress to web"
    }}

    chain output {{
        type filter hook output priority dstnat - 1; policy accept;
        ip daddr {config.destination} tcp dport {config.port} counter drop comment "drop local access to web"
    }}
}}
'''


def write_atomic(path: Path, content: str) -> None:
    if not path.name or path.name in {".", ".."}:
        raise GateError("invalid output path")
    try:
        directory_fd = os.open(
            path.parent,
            os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0),
        )
    except OSError as exc:
        raise GateError("cannot open output directory") from exc

    temporary: str | None = None
    try:
        try:
            current = os.stat(path.name, dir_fd=directory_fd, follow_symlinks=False)
        except FileNotFoundError:
            current = None
        if current is not None and (stat.S_ISLNK(current.st_mode) or not stat.S_ISREG(current.st_mode)):
            raise GateError("output must be a regular file, not a symlink")

        for _ in range(16):
            temporary = f".{path.name}.{os.getpid()}.{os.urandom(8).hex()}.tmp"
            try:
                fd = os.open(
                    temporary,
                    os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0),
                    0o600,
                    dir_fd=directory_fd,
                )
                break
            except FileExistsError:
                continue
        else:
            raise GateError("cannot publish output")
        try:
            os.fchmod(fd, 0o600)
            with os.fdopen(fd, "wb", closefd=True) as output:
                output.write(content.encode("utf-8"))
                output.flush()
                os.fsync(output.fileno())
        except Exception:
            try:
                os.close(fd)
            except OSError:
                pass
            raise

        try:
            latest = os.stat(path.name, dir_fd=directory_fd, follow_symlinks=False)
        except FileNotFoundError:
            latest = None
        if latest is not None and stat.S_ISLNK(latest.st_mode):
            raise GateError("output must be a regular file, not a symlink")
        if current is None and latest is not None:
            raise GateError("output changed during publication")
        if current is not None and (
            latest is None or (latest.st_dev, latest.st_ino) != (current.st_dev, current.st_ino)
        ):
            raise GateError("output changed during publication")

        os.replace(temporary, path.name, src_dir_fd=directory_fd, dst_dir_fd=directory_fd)
        temporary = None
        os.fsync(directory_fd)
    except GateError:
        raise
    except OSError as exc:
        raise GateError("cannot publish output") from exc
    finally:
        if temporary is not None:
            try:
                os.unlink(temporary, dir_fd=directory_fd)
            except FileNotFoundError:
                pass
        os.close(directory_fd)


def require_matching_output(path: Path, content: str) -> None:
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        fd = os.open(path, flags)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_ENV_BYTES:
                raise GateError("installed rules do not match")
            actual = os.read(fd, MAX_ENV_BYTES + 1)
        finally:
            os.close(fd)
    except GateError:
        raise
    except OSError as exc:
        raise GateError("installed rules do not match") from exc
    if actual != content.encode("utf-8"):
        raise GateError("installed rules do not match")


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("--env-file", type=Path)
    result.add_argument("--web-bind-host", dest="destination")
    result.add_argument("--tailscale-proxy-ipv4", dest="proxy")
    result.add_argument("--web-port", dest="port")
    result.add_argument("--docker-bridge-subnet", dest="subnet")
    result.add_argument("--docker-bridge-gateway", dest="gateway")
    result.add_argument("--docker-bridge-name", dest="bridge_name")
    result.add_argument("--web-container-ipv4", dest="web_container")
    result.add_argument("--output", type=Path)
    result.add_argument("--require-matching-output", type=Path)
    result.add_argument("--check", action="store_true", help="validate and render without writing")
    return result


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    try:
        values = read_dotenv(args.env_file) if args.env_file else {}
        rendered = render(config_from_inputs(args, values))
        if args.require_matching_output is not None:
            require_matching_output(args.require_matching_output, rendered)
        elif not args.check:
            if args.output is None:
                raise GateError("missing --output")
            write_atomic(args.output, rendered)
    except GateError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
