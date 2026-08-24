#!/usr/bin/env python3
"""Manage the exact UFW rules required by production web and model traffic."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import ipaddress
import os
import re
import subprocess
import sys
from pathlib import Path

import render_tailscale_web_gate as gate


WEB_COMMENT = "switchyard-web-gate"
MODEL_COMMENT = "switchyard-model-gate"
MODEL_PORT = "11435"


@dataclass(frozen=True)
class RuleCommands:
    name: str
    check: list[str]
    add: list[str]
    delete: list[str]


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


def commands(config: gate.GateConfig, api_container: ipaddress.IPv4Address) -> tuple[RuleCommands, RuleCommands]:
    web_rule = [
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
        "comment", WEB_COMMENT,
    ]
    model_rule = [
        "-i", config.bridge_name,
        "-p", "tcp",
        "-s", f"{api_container}/32",
        "-d", f"{config.gateway}/32",
        "--dport", MODEL_PORT,
        "-j", "ACCEPT",
    ]
    model_ufw_rule = [
        "allow", "in", "on", config.bridge_name,
        "proto", "tcp",
        "from", str(api_container),
        "to", str(config.gateway),
        "port", MODEL_PORT,
        "comment", MODEL_COMMENT,
    ]
    return (
        RuleCommands(
            "web",
            ["iptables", "-C", "ufw-user-forward", *web_rule],
            ["ufw", *ufw_rule],
            ["ufw", "--force", "route", "delete", "allow", *ufw_rule[2:]],
        ),
        RuleCommands(
            "model",
            ["iptables", "-C", "ufw-user-input", *model_rule],
            ["ufw", *model_ufw_rule],
            ["ufw", "--force", "delete", *model_ufw_rule],
        ),
    )


def api_container_from_values(values: dict[str, str], config: gate.GateConfig) -> ipaddress.IPv4Address:
    try:
        api_container = ipaddress.IPv4Address(values["SWITCHYARD_API_CONTAINER_IPV4"])
    except (KeyError, ValueError) as exc:
        raise gate.GateError("invalid SWITCHYARD_API_CONTAINER_IPV4") from exc
    if api_container not in config.subnet or api_container in {
        config.subnet.network_address,
        config.subnet.broadcast_address,
        config.gateway,
        config.web_container,
    }:
        raise gate.GateError("invalid SWITCHYARD_API_CONTAINER_IPV4")
    return api_container


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
    parser.add_argument("action", choices=("check-config", "verify", "verify-gate", "ensure", "delete", "delete-web", "delete-model"))
    parser.add_argument("--env-file", required=True, type=Path)
    args = parser.parse_args(argv)
    try:
        values = gate.read_dotenv(args.env_file)
        config_args = argparse.Namespace(
            destination=None, proxy=None, port=None, subnet=None, gateway=None,
            bridge_name=None, web_container=None,
        )
        config = gate.config_from_inputs(config_args, values)
        rules = commands(config, api_container_from_values(values, config))
        if args.action == "check-config":
            return 0
        require_ufw_active()
        present = {rule.name: run(rule.check, quiet=True).returncode == 0 for rule in rules}
        if args.action in {"verify", "verify-gate"}:
            missing = [rule.name for rule in rules if not present[rule.name]]
            if missing:
                raise gate.GateError(f"exact UFW {' and '.join(missing)} rule is missing")
            if args.action == "verify-gate":
                verify_nft(config)
            return 0
        if args.action.startswith("delete"):
            selected = rules if args.action == "delete" else tuple(rule for rule in rules if rule.name == args.action.removeprefix("delete-"))
            for rule in selected:
                if present[rule.name] and run(rule.delete).returncode != 0:
                    raise gate.GateError(f"could not delete exact UFW {rule.name} rule")
                if run(rule.check, quiet=True).returncode == 0:
                    raise gate.GateError(f"exact UFW {rule.name} rule remains")
            return 0
        created: list[RuleCommands] = []
        for rule in rules:
            if present[rule.name]:
                continue
            if run(rule.add, quiet=True).returncode != 0 or run(rule.check, quiet=True).returncode != 0:
                for created_rule in reversed(created):
                    run(created_rule.delete, quiet=True)
                run(rule.delete, quiet=True)
                raise gate.GateError(f"could not ensure exact UFW {rule.name} rule")
            created.append(rule)
        for rule in rules:
            print(f"{rule.name}={'created' if rule in created else 'existing'}")
        return 0
    except (gate.GateError, OSError) as exc:
        message = str(exc) if isinstance(exc, gate.GateError) else "required firewall command is unavailable"
        print(f"error: {message}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
