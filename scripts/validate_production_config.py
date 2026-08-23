#!/usr/bin/env python3
"""Validate the host-facing production transport contract from the environment."""

from __future__ import annotations

import argparse
import ipaddress
import os
import re
import sys
from collections.abc import Mapping
from pathlib import Path
from urllib.parse import urlsplit


TAILSCALE_CGNAT = ipaddress.IPv4Network("100.64.0.0/10")
DOTENV_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\Z")


def _value(env: Mapping[str, str], name: str, default: str = "") -> str:
    return env.get(name, default).strip()


def _port(env: Mapping[str, str], name: str, default: str, errors: list[str]) -> int | None:
    raw = _value(env, name, default)
    try:
        port = int(raw)
    except ValueError:
        errors.append(f"{name} must be an integer port")
        return None
    if not 1 <= port <= 65535:
        errors.append(f"{name} must be between 1 and 65535")
        return None
    return port


def parse_dotenv(path: Path) -> dict[str, str]:
    env: dict[str, str] = {}
    for line_number, raw_line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].lstrip()
        if "=" not in line:
            raise ValueError(f"invalid env file syntax at line {line_number}")
        name, raw_value = line.split("=", 1)
        name = name.strip()
        if not DOTENV_NAME.fullmatch(name):
            raise ValueError(f"invalid env file name at line {line_number}")

        value = raw_value.strip()
        if value.startswith(("'", '"')):
            quote = value[0]
            closing = value.find(quote, 1)
            trailing = value[closing + 1 :].strip()
            if closing < 0 or (trailing and not trailing.startswith("#")):
                raise ValueError(f"invalid quoted env value at line {line_number}")
            value = value[1:closing]
        else:
            value = re.split(r"\s+#", value, maxsplit=1)[0].rstrip()
        env[name] = value
    return env


def validate(env: Mapping[str, str]) -> list[str]:
    errors: list[str] = []
    transport = _value(env, "SWITCHYARD_WEB_TRANSPORT", "http").lower()
    web_bind: ipaddress.IPv4Address | None = None
    if transport not in {"http", "tailscale"}:
        errors.append("SWITCHYARD_WEB_TRANSPORT must be http or tailscale")
    elif transport != "tailscale":
        errors.append("production requires SWITCHYARD_WEB_TRANSPORT=tailscale")

    if transport == "tailscale":
        web_bind_raw = _value(env, "SWITCHYARD_WEB_BIND_HOST", "127.0.0.1")
        try:
            web_bind = ipaddress.IPv4Address(web_bind_raw)
            if web_bind not in TAILSCALE_CGNAT:
                errors.append(
                    "SWITCHYARD_WEB_BIND_HOST must be an IPv4 address within 100.64.0.0/10"
                )
        except ValueError:
            errors.append("SWITCHYARD_WEB_BIND_HOST must be an IPv4 address")

        proxy_raw = _value(env, "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR")
        proxy: ipaddress.IPv4Network | None = None
        if not proxy_raw.endswith("/32"):
            errors.append("SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be an explicit /32")
        else:
            try:
                proxy = ipaddress.IPv4Network(proxy_raw, strict=True)
                if not proxy.subnet_of(TAILSCALE_CGNAT):
                    errors.append(
                        "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be within 100.64.0.0/10"
                    )
            except ValueError:
                errors.append(
                    "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be a valid IPv4 /32"
                )

        if web_bind is not None and proxy is not None and web_bind == proxy.network_address:
            errors.append(
                "SWITCHYARD_WEB_BIND_HOST and SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must identify distinct Tailscale peers"
            )

    dashboard_raw = _value(env, "SWITCHYARD_DASHBOARD_CIDR", "127.0.0.1/32")
    try:
        dashboard = ipaddress.IPv4Network(dashboard_raw, strict=True)
        if (
            dashboard.prefixlen != 32
            or not dashboard_raw.endswith("/32")
            or not dashboard.network_address.is_loopback
        ):
            raise ValueError
    except ValueError:
        errors.append("SWITCHYARD_DASHBOARD_CIDR must be a literal loopback IPv4 /32")

    public_url = _value(env, "SWITCHYARD_PUBLIC_URL")
    try:
        parsed_url = urlsplit(public_url)
        parsed_url.port
        valid_public_url = (
            parsed_url.scheme.lower() == "https"
            and bool(parsed_url.hostname)
            and parsed_url.username is None
            and parsed_url.password is None
            and parsed_url.path in {"", "/"}
            and not parsed_url.query
            and not parsed_url.fragment
        )
    except ValueError:
        valid_public_url = False
    if not valid_public_url:
        errors.append("SWITCHYARD_PUBLIC_URL must be an absolute HTTPS URL")

    if _value(env, "SWITCHYARD_REVERSE_PROXY_AUTH", "false").lower() != "true":
        errors.append("SWITCHYARD_REVERSE_PROXY_AUTH must be true")

    control_bind = _value(env, "SWITCHYARD_BIND_HOST", "127.0.0.1")
    control_ip: ipaddress.IPv4Address | None = None
    try:
        control_ip = ipaddress.IPv4Address(control_bind)
        if not control_ip.is_loopback:
            raise ValueError
    except ValueError:
        control_ip = None
        errors.append("SWITCHYARD_BIND_HOST must be a literal loopback IPv4 address")

    model_bind = _value(env, "SWITCHYARD_MODEL_BIND_HOST", control_bind)
    model_ip: ipaddress.IPv4Address | None = None
    try:
        model_ip = ipaddress.IPv4Address(model_bind)
        if model_ip.is_unspecified:
            raise ValueError
    except ValueError:
        model_ip = None
        errors.append(
            "SWITCHYARD_MODEL_BIND_HOST must be a literal non-unspecified IPv4 address"
        )

    postgres_port = _port(env, "POSTGRES_PORT", "5432", errors)
    control_port = _port(env, "API_PORT", "18080", errors)
    model_port = _port(env, "LLAMA_LINE_PORT", "11434", errors)
    web_port = _port(env, "WEB_PORT", "8081", errors)
    listeners = (
        ("postgres", control_ip, postgres_port),
        ("control", control_ip, control_port),
        ("model", model_ip, model_port),
        ("web", web_bind, web_port),
    )
    for index, (first_name, first_bind, first_port) in enumerate(listeners):
        for second_name, second_bind, second_port in listeners[index + 1 :]:
            if (
                first_bind is not None
                and second_bind is not None
                and first_port is not None
                and first_port == second_port
                and first_bind == second_bind
            ):
                errors.append(f"{first_name} and {second_name} listeners must not collide")

    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--env-file", type=Path)
    args = parser.parse_args()

    env: dict[str, str] = {}
    if args.env_file is not None:
        try:
            env.update(parse_dotenv(args.env_file))
        except OSError:
            print("production config: unable to read env file", file=sys.stderr)
            return 1
        except UnicodeError:
            print("production config: env file must be UTF-8", file=sys.stderr)
            return 1
        except ValueError as error:
            print(f"production config: {error}", file=sys.stderr)
            return 1
    env.update(os.environ)

    errors = validate(env)
    if errors:
        for error in errors:
            print(f"production config: {error}", file=sys.stderr)
        return 1
    print("Production configuration contract passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
