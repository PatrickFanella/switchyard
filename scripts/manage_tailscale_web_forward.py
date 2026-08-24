#!/usr/bin/env python3
"""Manage the exact UFW post-DNAT route for the production web listener."""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from pathlib import Path

import render_tailscale_web_gate as gate


COMMENT = "switchyard-web-gate"


def verify_nft(config: gate.GateConfig) -> None:
    result = subprocess.run(
        ["nft", "list", "table", "inet", "switchyard_web_gate"],
        capture_output=True, text=True, env={**os.environ, "LC_ALL": "C"}, check=False,
    )
    if result.returncode != 0:
        raise gate.GateError("live nft web gate is missing")
    expected = [" ".join(line.split()) for line in gate.render(config).splitlines() if " counter " in line]
    live = [" ".join(line.split()) for line in result.stdout.splitlines()]
    if len(expected) != 3 or live.count("table inet switchyard_web_gate {") != 1:
        raise gate.GateError("live nft web gate does not match")
    chains = {"prerouting": False, "output": False}
    hooks = {"prerouting": False, "output": False}
    current = None
    live_rules: list[str] = []
    for line in live:
        match = re.fullmatch(r"chain (prerouting|output) \{", line)
        if match:
            current = match.group(1)
            if chains[current]:
                raise gate.GateError("live nft web gate does not match")
            chains[current] = True
        elif line == "}":
            current = None
        elif line.startswith("type filter hook "):
            if current is None or hooks[current] or re.fullmatch(
                rf"type filter hook {current} priority (?:dstnat - 1|-101); policy accept;", line
            ) is None:
                raise gate.GateError("live nft web gate does not match")
            hooks[current] = True
        elif line not in {"", "table inet switchyard_web_gate {"}:
            live_rules.append(line)
    if not all(chains.values()) or not all(hooks.values()):
        raise gate.GateError("live nft web gate does not match")
    if len(live_rules) != 3:
        raise gate.GateError("live nft web gate does not match")
    for rule in expected:
        prefix, suffix = rule.split(" counter ", 1)
        pattern = re.compile(rf"{re.escape(prefix)} counter packets [0-9]+ bytes [0-9]+ {re.escape(suffix)}")
        if sum(pattern.fullmatch(line) is not None for line in live_rules) != 1:
            raise gate.GateError("live nft web gate does not match")


def commands(config: gate.GateConfig) -> tuple[list[str], list[str], list[str]]:
    rule = [
        "-i", "tailscale0",
        "-o", config.bridge_name,
        "-p", "tcp",
        "-s", f"{config.proxy}/32",
        "-d", f"{config.web_container}/32",
        "--dport", "80",
        "-j", "ACCEPT",
    ]
    ufw_rule = [
        "route", "allow",
        "in", "on", "tailscale0",
        "out", "on", config.bridge_name,
        "proto", "tcp",
        "from", str(config.proxy),
        "to", str(config.web_container),
        "port", "80",
        "comment", COMMENT,
    ]
    return ["iptables", "-C", "ufw-user-forward", *rule], ["ufw", *ufw_rule], ["ufw", "--force", "route", "delete", "allow", *ufw_rule[2:]]


def run(command: list[str], *, quiet: bool = False) -> subprocess.CompletedProcess[str]:
    environment = os.environ.copy()
    environment["LC_ALL"] = "C"
    return subprocess.run(
        command,
        text=True,
        stdout=subprocess.DEVNULL if quiet else None,
        stderr=subprocess.DEVNULL if quiet else None,
        env=environment,
        check=False,
    )


def require_ufw_active() -> None:
    result = subprocess.run(
        ["ufw", "status"], capture_output=True, text=True, env={**os.environ, "LC_ALL": "C"}, check=False
    )
    if result.returncode != 0 or not result.stdout.startswith("Status: active\n"):
        raise gate.GateError("UFW must be active")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("check-config", "verify", "verify-gate", "ensure", "delete"))
    parser.add_argument("--env-file", required=True, type=Path)
    args = parser.parse_args(argv)
    try:
        values = gate.read_dotenv(args.env_file)
        config_args = argparse.Namespace(
            destination=None, proxy=None, port=None, subnet=None, gateway=None,
            bridge_name=None, web_container=None,
        )
        config = gate.config_from_inputs(config_args, values)
        check, add, delete = commands(config)
        if args.action == "check-config":
            return 0
        require_ufw_active()
        present = run(check, quiet=True).returncode == 0
        if args.action in {"verify", "verify-gate"}:
            if not present:
                raise gate.GateError("exact UFW web forward rule is missing")
            if args.action == "verify-gate":
                verify_nft(config)
            return 0
        if args.action == "delete":
            if present and run(delete).returncode != 0:
                raise gate.GateError("could not delete exact UFW web forward rule")
            if run(check, quiet=True).returncode == 0:
                raise gate.GateError("exact UFW web forward rule remains")
            return 0
        if present:
            print("existing")
            return 0
        if run(add).returncode != 0:
            raise gate.GateError("could not add exact UFW web forward rule")
        if run(check, quiet=True).returncode != 0:
            run(delete, quiet=True)
            raise gate.GateError("exact UFW web forward rule is ineffective")
        print("created")
        return 0
    except (gate.GateError, OSError) as exc:
        message = str(exc) if isinstance(exc, gate.GateError) else "required firewall command is unavailable"
        print(f"error: {message}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
