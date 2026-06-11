#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from typing import Any

import yaml


def request(method: str, url: str, body: dict[str, Any] | None = None, headers: dict[str, str] | None = None) -> Any:
    data = None if body is None else json.dumps(body).encode()
    req_headers = {"Accept": "application/json"}
    if data is not None:
        req_headers["Content-Type"] = "application/json"
    if headers:
        req_headers.update(headers)
    req = urllib.request.Request(url, data=data, headers=req_headers, method=method)
    with urllib.request.urlopen(req, timeout=60) as resp:
        raw = resp.read()
        if not raw:
            return None
        return json.loads(raw)


def now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://10.0.0.200:6006")
    parser.add_argument("--broker-url", default="http://127.0.0.1:11434")
    parser.add_argument("--config", default="config.yaml")
    args = parser.parse_args()

    cfg = yaml.safe_load(open(args.config, "r", encoding="utf-8"))
    keys = {item["name"]: item["key"] for item in cfg.get("keys", [])}
    api_key = keys.get("subcorp")
    if not api_key:
        raise SystemExit("subcorp API key missing from config")

    base = args.base_url.rstrip("/")
    datasets = request("GET", base + "/v1/datasets")["data"]
    dataset = next(d for d in datasets if d["name"] == "smoke-tests")
    examples = request("GET", base + f"/v1/datasets/{urllib.parse.quote(dataset['id'])}/examples")["data"]["examples"]
    example = examples[0]
    experiments = request("GET", base + f"/v1/datasets/{urllib.parse.quote(dataset['id'])}/experiments")["data"]
    experiment = next(e for e in experiments if e.get("metadata", {}).get("managed_by") == "llama-line phoenix_bootstrap.py")

    start = now()
    body = {"model": "qwen3-embedding:0.6b", "input": json.dumps(example["input"], sort_keys=True)}
    broker_req = urllib.request.Request(
        args.broker_url.rstrip("/") + "/v1/embeddings",
        data=json.dumps(body).encode(),
        headers={"Authorization": "Bearer " + api_key, "Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(broker_req, timeout=60) as resp:
            output = {"http_status": resp.status, "bytes": len(resp.read())}
            error = None
    except Exception as exc:  # noqa: BLE001
        output = {"http_status": 0}
        error = str(exc)
    end = now()

    run = request(
        "POST",
        base + f"/v1/experiments/{urllib.parse.quote(experiment['id'])}/runs",
        {
            "dataset_example_id": example["id"],
            "output": output,
            "repetition_number": 1,
            "start_time": start,
            "end_time": end,
            "error": error,
        },
    )["data"]
    eval_start = now()
    time.sleep(0.01)
    evaluation = request(
        "POST",
        base + "/v1/experiment_evaluations",
        {
            "experiment_run_id": run["id"],
            "name": "quality",
            "annotator_kind": "CODE",
            "start_time": eval_start,
            "end_time": now(),
            "result": {
                "label": "pass" if error is None else "fail",
                "score": 1.0 if error is None else 0.0,
                "explanation": "broker smoke request returned 200" if error is None else error,
            },
            "metadata": {"managed_by": "llama-line phoenix_eval_smoke.py"},
        },
    )["data"]
    print(json.dumps({"experiment_run_id": run["id"], "evaluation_id": evaluation["id"], "output": output}, indent=2))
    return 0 if error is None else 1


if __name__ == "__main__":
    raise SystemExit(main())
