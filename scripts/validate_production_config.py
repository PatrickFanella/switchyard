#!/usr/bin/env python3
"""Validate the host-facing production transport contract from the environment."""

from __future__ import annotations

import ipaddress
import os
import sys
from collections.abc import Mapping
from urllib.parse import urlsplit


TAILSCALE_CGNAT = ipaddress.IPv4Network("100.64.0.0/10")


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


def _binds_overlap(first: str, second: str) -> bool:
    aliases = {"localhost": "127.0.0.1"}
    first = aliases.get(first.lower(), first)
    second = aliases.get(second.lower(), second)
    try:
        first_ip = ipaddress.ip_address(first)
        second_ip = ipaddress.ip_address(second)
    except ValueError:
        return first.lower() == second.lower()
    if first_ip.version != second_ip.version:
        return False
    return first_ip == second_ip or first_ip.is_unspecified or second_ip.is_unspecified


def validate(env: Mapping[str, str]) -> list[str]:
    errors: list[str] = []
    transport = _value(env, "SWITCHYARD_WEB_TRANSPORT", "http").lower()
    if transport not in {"http", "tailscale"}:
        errors.append("SWITCHYARD_WEB_TRANSPORT must be http or tailscale")
    elif transport != "tailscale":
        errors.append("production requires SWITCHYARD_WEB_TRANSPORT=tailscale")

    if transport == "tailscale":
        web_bind_raw = _value(env, "SWITCHYARD_WEB_BIND_HOST", "127.0.0.1")
        web_bind: ipaddress.IPv4Address | None = None
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
        if dashboard.prefixlen != 32 or not dashboard_raw.endswith("/32"):
            raise ValueError
    except ValueError:
        errors.append("SWITCHYARD_DASHBOARD_CIDR must be a valid explicit IPv4 /32")

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
    if control_bind.lower() != "localhost":
        try:
            if not ipaddress.ip_address(control_bind).is_loopback:
                errors.append("SWITCHYARD_BIND_HOST must be loopback")
        except ValueError:
            errors.append("SWITCHYARD_BIND_HOST must be a loopback IP address")

    model_bind = _value(env, "SWITCHYARD_MODEL_BIND_HOST", control_bind)
    control_port = _port(env, "API_PORT", "18080", errors)
    model_port = _port(env, "LLAMA_LINE_PORT", "11434", errors)
    if (
        control_port is not None
        and model_port is not None
        and control_port == model_port
        and _binds_overlap(control_bind, model_bind)
    ):
        errors.append("control and model listeners must not collide")

    return errors


def main() -> int:
    errors = validate(os.environ)
    if errors:
        for error in errors:
            print(f"production config: {error}", file=sys.stderr)
        return 1
    print("Production configuration contract passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
