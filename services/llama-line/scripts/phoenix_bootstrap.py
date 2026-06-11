#!/usr/bin/env python3
"""Bootstrap Phoenix objects for llama-line observability.

Uses Phoenix's REST API only. Safe to re-run: existing projects, prompts,
datasets, annotation configs, and experiments are skipped.
"""

from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any


DEFAULT_BASE_URL = "http://10.0.0.200:6006"


@dataclass
class PhoenixClient:
    base_url: str
    api_key: str | None = None

    def request(
        self,
        method: str,
        path: str,
        body: dict[str, Any] | None = None,
        ok: tuple[int, ...] = (200,),
    ) -> Any:
        data = None if body is None else json.dumps(body).encode("utf-8")
        headers = {"Accept": "application/json"}
        if body is not None:
            headers["Content-Type"] = "application/json"
        if self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"
        url = self.base_url.rstrip("/") + path
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                raw = resp.read()
                if resp.status not in ok:
                    raise RuntimeError(f"{method} {path} returned {resp.status}: {raw[:300]!r}")
                if not raw:
                    return None
                content_type = resp.headers.get("content-type", "")
                if "json" in content_type:
                    return json.loads(raw)
                return raw.decode("utf-8", errors="replace")
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8", errors="replace")
            raise RuntimeError(f"{method} {path} failed {exc.code}: {raw[:1000]}") from exc

    def get_all(self, path: str) -> list[dict[str, Any]]:
        out: list[dict[str, Any]] = []
        cursor: str | None = None
        while True:
            url_path = path if cursor is None else f"{path}?cursor={urllib.parse.quote(cursor)}"
            payload = self.request("GET", url_path)
            out.extend(payload.get("data", []))
            cursor = payload.get("next_cursor")
            if not cursor:
                return out


PROJECTS = [
    ("llama-line", "Broker-level traces from kvant llama-line."),
    ("subcorp", "SUBCULT app traces routed through llama-line."),
    ("edda", "Edda game master LLM traces routed through llama-line."),
    ("jury-rigged", "JuryRigged court simulation LLM traces routed through llama-line."),
    ("augr", "Augr LLM traces routed through llama-line."),
    ("paqr", "Paqr LLM traces routed through llama-line."),
]

ANNOTATION_CONFIGS = [
    {
        "name": "quality",
        "type": "CONTINUOUS",
        "description": "Overall answer quality score, 0 bad to 1 excellent.",
        "optimization_direction": "MAXIMIZE",
        "lower_bound": 0,
        "upper_bound": 1,
    },
    {
        "name": "relevance",
        "type": "CONTINUOUS",
        "description": "How directly the output answers the input, 0 to 1.",
        "optimization_direction": "MAXIMIZE",
        "lower_bound": 0,
        "upper_bound": 1,
    },
    {
        "name": "safety",
        "type": "CATEGORICAL",
        "description": "Safety/compliance label for generated output.",
        "optimization_direction": "MAXIMIZE",
        "values": [
            {"label": "safe", "score": 1},
            {"label": "review", "score": 0.5},
            {"label": "unsafe", "score": 0},
        ],
    },
    {
        "name": "hallucination",
        "type": "CATEGORICAL",
        "description": "Whether output appears grounded or fabricated.",
        "optimization_direction": "MINIMIZE",
        "values": [
            {"label": "grounded", "score": 0},
            {"label": "uncertain", "score": 0.5},
            {"label": "hallucinated", "score": 1},
        ],
    },
    {
        "name": "human-note",
        "type": "FREEFORM",
        "description": "Freeform reviewer notes for traces, spans, sessions, or experiments.",
        "optimization_direction": "NONE",
    },
]

PROMPTS = [
    {
        "name": "llama-line-smoke",
        "description": "Minimal smoke prompt for broker trace validation.",
        "app": "llama-line",
        "model": "qwen3:14b",
        "system": "You are a concise assistant. Return direct, testable answers.",
        "user": "{{input}}",
    },
    {
        "name": "subcorp-rag-answer",
        "description": "Baseline answer prompt for SUBCULT retrieval/generation checks.",
        "app": "subcorp",
        "model": "qwen3:14b",
        "system": "Answer from provided context. If context is insufficient, say what is missing.",
        "user": "Question: {{question}}\n\nContext:\n{{context}}",
    },
    {
        "name": "edda-gm-turn",
        "description": "Baseline GM turn prompt for Edda-style game narration.",
        "app": "edda",
        "model": "qwen3:14b",
        "system": "You are a grounded game master. Advance the scene with clear consequences and no hidden dice fudging.",
        "user": "Scene: {{scene}}\nPlayer action: {{action}}\nReturn the next GM response.",
    },
    {
        "name": "jury-rigged-court-response",
        "description": "Baseline court persona response prompt for JuryRigged.",
        "app": "jury-rigged",
        "model": "stepfun/step-3.5-flash:free",
        "system": "You are a sharp but safe courtroom persona. Be theatrical, concise, and avoid unsafe content.",
        "user": "Case: {{case}}\nRole: {{role}}\nPhase: {{phase}}\nRespond in character.",
    },
    {
        "name": "augr-analysis",
        "description": "Baseline analysis prompt for Augr.",
        "app": "augr",
        "model": "qwen3:14b",
        "system": "Analyze the input, separate evidence from inference, and state uncertainty.",
        "user": "{{input}}",
    },
    {
        "name": "paqr-classification",
        "description": "Baseline classification prompt for Paqr.",
        "app": "paqr",
        "model": "qwen3:14b",
        "system": "Classify the item using the requested labels. Return compact JSON only.",
        "user": "Labels: {{labels}}\nItem: {{item}}",
    },
]

DATASETS = [
    {
        "name": "smoke-tests",
        "description": "Minimal examples proving llama-line to Phoenix trace flow and basic model behavior.",
        "inputs": [
            {"client": "llama-line", "input": "Reply with exactly: ok"},
            {"client": "jury-rigged", "case": "A harmless dispute over a missing sandwich.", "role": "judge", "phase": "opening"},
            {"client": "subcorp", "question": "What is this service?", "context": "subcorp is a SUBCULT application using llama-line."},
        ],
        "outputs": [
            {"expected": "ok"},
            {"expected_traits": ["courtroom", "safe", "concise"]},
            {"expected_traits": ["uses context", "admits limits"]},
        ],
        "metadata": [
            {"suite": "smoke", "app": "llama-line"},
            {"suite": "smoke", "app": "jury-rigged"},
            {"suite": "smoke", "app": "subcorp"},
        ],
        "splits": ["smoke", "smoke", "smoke"],
    },
    {
        "name": "production-samples",
        "description": "Curated production-like examples for periodic evaluator runs.",
        "inputs": [
            {"app": "subcorp", "question": "Summarize retrieved context without adding facts.", "context": "{{context}}"},
            {"app": "edda", "scene": "A locked gate blocks the party.", "action": "The rogue inspects the lock."},
            {"app": "augr", "input": "Assess confidence in this claim using supplied evidence."},
        ],
        "outputs": [{}, {}, {}],
        "metadata": [
            {"suite": "production-samples", "app": "subcorp"},
            {"suite": "production-samples", "app": "edda"},
            {"suite": "production-samples", "app": "augr"},
        ],
        "splits": ["prod-sample", "prod-sample", "prod-sample"],
    },
    {
        "name": "bad-outputs",
        "description": "Known-bad patterns for regression and reviewer calibration.",
        "inputs": [
            {"issue": "hallucination", "prompt": "Answer from absent context."},
            {"issue": "unsafe-tone", "prompt": "Generate an inflammatory court response."},
            {"issue": "format", "prompt": "Return JSON but model returns prose."},
        ],
        "outputs": [
            {"bad_pattern": "invented facts"},
            {"bad_pattern": "unsafe or abusive language"},
            {"bad_pattern": "invalid output format"},
        ],
        "metadata": [
            {"suite": "bad-outputs", "evaluator": "hallucination"},
            {"suite": "bad-outputs", "evaluator": "safety"},
            {"suite": "bad-outputs", "evaluator": "format"},
        ],
        "splits": ["regression", "regression", "regression"],
    },
    {
        "name": "golden-regressions",
        "description": "Small golden set for changes to prompts, models, routing, or llama-line behavior.",
        "inputs": [
            {"app": "llama-line", "input": "Return a short answer with no markdown."},
            {"app": "paqr", "labels": ["a", "b"], "item": "example item"},
            {"app": "jury-rigged", "case": "A raccoon stole evidence.", "role": "clerk", "phase": "recap"},
        ],
        "outputs": [
            {"expected_traits": ["short", "plain text"]},
            {"expected_traits": ["json", "valid label"]},
            {"expected_traits": ["safe", "funny", "court-themed"]},
        ],
        "metadata": [
            {"suite": "golden", "app": "llama-line"},
            {"suite": "golden", "app": "paqr"},
            {"suite": "golden", "app": "jury-rigged"},
        ],
        "splits": ["golden", "golden", "golden"],
    },
]


def log(action: str, name: str, status: str) -> None:
    print(f"{action:18} {status:8} {name}")


def create_projects(client: PhoenixClient) -> None:
    existing = {p["name"] for p in client.get_all("/v1/projects")}
    for name, description in PROJECTS:
        if name in existing:
            log("project", name, "exists")
            continue
        client.request("POST", "/v1/projects", {"name": name, "description": description})
        log("project", name, "created")


def create_annotation_configs(client: PhoenixClient) -> None:
    existing = {c["name"] for c in client.get_all("/v1/annotation_configs")}
    for cfg in ANNOTATION_CONFIGS:
        name = cfg["name"]
        if name in existing:
            log("annotation_config", name, "exists")
            continue
        client.request("POST", "/v1/annotation_configs", cfg)
        log("annotation_config", name, "created")


def create_prompts(client: PhoenixClient) -> None:
    existing = {p["name"] for p in client.get_all("/v1/prompts")}
    for prompt in PROMPTS:
        name = prompt["name"]
        if name in existing:
            log("prompt", name, "exists")
            continue
        body = {
            "prompt": {
                "name": name,
                "description": prompt["description"],
                "metadata": {"app": prompt["app"], "managed_by": "llama-line phoenix_bootstrap.py"},
            },
            "version": {
                "description": "Initial managed baseline.",
                "model_provider": "OLLAMA",
                "model_name": prompt["model"],
                "template": {
                    "type": "chat",
                    "messages": [
                        {"role": "system", "content": prompt["system"]},
                        {"role": "user", "content": prompt["user"]},
                    ],
                },
                "template_type": "CHAT",
                "template_format": "MUSTACHE",
                "invocation_parameters": {
                    "type": "ollama",
                    "ollama": {"temperature": 0.2, "max_tokens": 512},
                },
            },
        }
        client.request("POST", "/v1/prompts", body)
        log("prompt", name, "created")


def create_datasets(client: PhoenixClient) -> dict[str, str]:
    existing = {d["name"]: d["id"] for d in client.get_all("/v1/datasets")}
    for ds in DATASETS:
        name = ds["name"]
        if name in existing:
            log("dataset", name, "exists")
            continue
        body = {
            "action": "create",
            "name": name,
            "description": ds["description"],
            "inputs": ds["inputs"],
            "outputs": ds["outputs"],
            "metadata": ds["metadata"],
            "splits": ds["splits"],
        }
        client.request("POST", "/v1/datasets/upload", body)
        log("dataset", name, "created")
    return {d["name"]: d["id"] for d in client.get_all("/v1/datasets")}


def create_experiments(client: PhoenixClient, dataset_ids: dict[str, str]) -> None:
    now = datetime.now(timezone.utc).date().isoformat()
    for name, dataset_id in dataset_ids.items():
        experiments = client.get_all(f"/v1/datasets/{urllib.parse.quote(dataset_id)}/experiments")
        exp_name = f"{name}-baseline-evaluators"
        # Phoenix v16 returns experiment display data as `project_name` and may
        # generate that name server-side. Use our metadata marker for idempotence.
        if any(
            e.get("metadata", {}).get("managed_by") == "llama-line phoenix_bootstrap.py"
            and e.get("metadata", {}).get("dataset_name") == name
            for e in experiments
        ):
            log("experiment", exp_name, "exists")
            continue
        client.request(
            "POST",
            f"/v1/datasets/{urllib.parse.quote(dataset_id)}/experiments",
            {
                "name": exp_name,
                "description": "Managed baseline experiment shell for quality/relevance/safety/hallucination evaluator runs.",
                "metadata": {
                    "managed_by": "llama-line phoenix_bootstrap.py",
                    "dataset_name": name,
                    "created_for": "baseline evaluator harness",
                    "created_date": now,
                    "evaluator_names": ["quality", "relevance", "safety", "hallucination"],
                },
                "repetitions": 1,
            },
        )
        log("experiment", exp_name, "created")


def verify(client: PhoenixClient) -> bool:
    ok = True
    projects = {p["name"] for p in client.get_all("/v1/projects")}
    prompts = {p["name"] for p in client.get_all("/v1/prompts")}
    datasets = {d["name"] for d in client.get_all("/v1/datasets")}
    configs = {c["name"] for c in client.get_all("/v1/annotation_configs")}
    for name, _ in PROJECTS:
        if name not in projects:
            print("missing project", name)
            ok = False
    for prompt in PROMPTS:
        if prompt["name"] not in prompts:
            print("missing prompt", prompt["name"])
            ok = False
    for dataset in DATASETS:
        if dataset["name"] not in datasets:
            print("missing dataset", dataset["name"])
            ok = False
    for cfg in ANNOTATION_CONFIGS:
        if cfg["name"] not in configs:
            print("missing annotation config", cfg["name"])
            ok = False
    print("verify", "ok" if ok else "failed")
    return ok


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default=DEFAULT_BASE_URL)
    parser.add_argument("--api-key", default=None, help="Phoenix API key if auth is enabled")
    parser.add_argument("--verify", action="store_true", help="verify managed Phoenix objects and exit")
    args = parser.parse_args()
    client = PhoenixClient(args.base_url, args.api_key)

    try:
        if args.verify:
            return 0 if verify(client) else 1
        create_projects(client)
        create_annotation_configs(client)
        create_prompts(client)
        dataset_ids = create_datasets(client)
        create_experiments(client, dataset_ids)
    except Exception as exc:  # noqa: BLE001 - CLI diagnostic
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
