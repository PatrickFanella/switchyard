# Webhook ingress

Switchyard can expose one opt-in webhook-only listener. It is disabled and unpublished by default. Never share its address with the control or model listener.

Set `SWITCHYARD_WEBHOOK_LISTEN_ADDR`, `SWITCHYARD_WEBHOOK_CONFIG_FILE`, `SWITCHYARD_WEBHOOK_SECRET_DIR`, and `SWITCHYARD_DEPLOYMENT_ID` to enable it. The config owner must equal the deployment ID. Switchyard opens every path component with `O_DIRECTORY|O_NOFOLLOW`, then opens files relative to the retained directory descriptor with `openat(O_NOFOLLOW)`. It checks owner, mode, type, and size once with `fstat` and reads bounded bytes from that same descriptor. The config file must be absolute, regular, process-owned, and mode `0400` or `0600`. The secret directory must be absolute, process-owned, and mode `0700`. Secret files use safe basenames and must be regular, process-owned, mode `0400`, 32-4096 bytes, and contain the exact HMAC key bytes. Secret bytes and file references never enter the database or API.

The base Compose file does not publish the listener. To opt in, set `SWITCHYARD_DEPLOYMENT_ID`, `SWITCHYARD_WEBHOOK_CONFIG_HOST_FILE`, and `SWITCHYARD_WEBHOOK_SECRET_HOST_DIR`, then run:

```bash
docker compose -f docker-compose.yml -f deploy/docker-compose.webhook.yml.example up -d
```

The override publishes `127.0.0.1:18082` by default and mounts both inputs read-only. Prepare the host paths before startup. Their owner UID must match `OPENCODE_RUNTIME_UID`, which is also the API container UID. Set `SWITCHYARD_WEBHOOK_BIND_HOST` or `SWITCHYARD_WEBHOOK_PORT` only when the loopback default is unsuitable.

## Configure a trigger

The trigger must already exist with type `webhook`, be enabled, and refer to an active workflow definition. Static trigger inputs and projected inputs must be disjoint and satisfy that definition's scalar `input_shape`. Sensitive input names are rejected.

```json
{
  "version": 1,
  "owner": "production-a",
  "generation": 7,
  "receipt_retention": "2160h",
  "triggers": [{
    "trigger_id": "gitea-push",
    "revision": "2026-08-24-a",
    "keys": [{
      "id": "current",
      "secret_file": "gitea-push.key",
      "not_before": "2026-08-24T00:00:00Z",
      "not_after": "2026-09-24T00:00:00Z"
    }],
    "projections": {
      "repo": {"pointer": "/repository/full_name"},
      "ref": {"pointer": "/ref"}
    }
  }]
}
```

The file is the complete authoritative trigger set for one owner. Increase `generation` for every change. The store rejects lower generations, another payload at the same generation, and another owner. An exact same-generation reload is idempotent. A newer generation transaction locks the sorted union of incoming and previously configured trigger IDs before replacement, so omitted triggers cannot race admission.

`receipt_retention` must be from 24 hours through 366 days and at least `SWITCHYARD_RETENTION`. JSON Pointer projections must resolve to strings, numbers, or booleans. Pointer segments that decode to sensitive names are rejected even when the destination input has a benign name. At most 32 projections are allowed. Each encoded scalar is capped at 4096 bytes and all encoded run inputs are capped at 16 KiB. The handler and atomic store admission both enforce these limits. One or two keys are allowed. Two validity windows may overlap by at most 24 hours.

## Sign a delivery

Send parameter-free `Content-Type: application/json`, absent or `identity` content encoding, and exactly one of each header:

- `X-Switchyard-Delivery`: producer-unique ID, at most 128 characters.
- `X-Switchyard-Timestamp`: canonical Unix seconds within five minutes.
- `X-Switchyard-Key-Id`: configured key ID.
- `X-Switchyard-Signature`: lowercase `v1=` plus 64 SHA-256 hex characters.

Sign these exact UTF-8 bytes, with no final newline:

```text
switchyard-hmac-v1
trigger:{triggerID}
delivery:{deliveryID}
timestamp:{unixSeconds}
body-sha256:{lowercaseSHA256OfExactBody}
```

Switchyard reads the bounded body and verifies HMAC before taking a global authenticated-admission slot. Slow unauthenticated clients are constrained by server read timeouts and the small per-client pre-auth rate limiter, but cannot consume admission slots. Unknown triggers and unknown keys run dummy HMAC verification and return the same `401` response as known triggers with invalid authentication. Bodies are capped at 64 KiB. The admission timeout must remain below the server write timeout.

## Replay behavior

Admission locks and rechecks trigger type, enabled state, config revision, key validity using the database clock, and the active workflow. A new `(trigger_id, delivery_id)` atomically creates one receipt, immutable pipeline snapshot, queued run, `run.created`, and `webhook.accepted` event. Before receipt expiry, an identical body digest returns `200` and the original run. The same delivery ID with a different digest returns `409`.

At expiry, admission removes the old receipt under the same lock and treats the delivery as new. Reusing the delivery ID then creates a new run, regardless of the old digest. Retention pruning removes expired receipts in bounded locked batches. `switchyard_webhook_receipts_retained` reports the current ledger size and `switchyard_webhook_receipts_pruned_total` counts admission-time and background removals. Ordinary workers claim queued runs; the scheduler ignores webhook triggers.

Only the body SHA-256 digest and delivery ID are retained in the receipt ledger. Raw payloads, signatures, request headers, secret bytes, and secret-file references are not persisted or logged.
