# Step outputs

Generic linear workflows can consume earlier step results instead of only their
original run inputs. A step publishes a bounded set of named string outputs;
later steps bind them in input templates. Outputs are persisted on the run step
record and restored on retry, resume, and restart without rerunning succeeded
steps.

## Reference syntax

Inside any pipeline step `inputs` value:

```
{{steps.<step_id>.outputs.<name>}}
```

- `<step_id>` is the pipeline step `id` in the run's immutable pipeline snapshot.
- `<name>` starts with a lowercase letter, contains only lowercase letters,
  digits, and underscores, is at most 64 characters, and must not
  contain a credential marker (`password`, `secret`, `token`, `authorization`,
  `api_key`, `apikey`, `credential`).
- The referenced step must run earlier (lower `position`). A step cannot
  reference its own outputs.
- Any other `{{steps.…}}` shape is rejected; it never renders as literal text.

Run-input templates (`{{key}}`) are unchanged and are rendered after step
references, so an output value can also be assembled from run inputs.

Validation happens twice:

1. `PUT /api/workflow-definitions/{id}` rejects (`400`) references to unknown
   steps, references to the same or a later step, malformed references, and
   internal-step `output.<name>` keys with invalid names.
2. At run time the runner resolves every reference before the step executes.
   A missing output (for example the producing step published no value with
   that name) fails the consuming step with
   `unresolved step output reference "steps.<id>.outputs.<name>": …`, records
   the step as `failed`, and pauses the run. The failure is deterministic, so it
   is classified non-retryable; no executor side effect runs.

## Published outputs by executor

| Executor | Output | Value |
|---|---|---|
| `internal` | `summary` | The step summary (`summary`/`template` input or the default text). |
| `internal` | `<name>` | Each input `output.<name>` after template rendering. |
| `internal` | `artifact_name` | Name of the attached artifact when one was produced. |
| `model` | `content` | The model response text. |
| `model` | `content_bytes`, `content_sha256` | Size and SHA-256 of the full response. |
| `model` | `model` | The resolved model identifier. |
| `model` | `artifact_name` | `<step_id>.md`. |
| `command` | `output` | Command output (empty for dry-run decisions). |
| `command` | `output_bytes`, `output_sha256` | Size and SHA-256 of the full output. |
| `command` | `command`, `executed`, `policy_reason` | The evaluated command, `true`/`false`, and the allowlist decision reason. |
| `http` | `status` | Response status code. |
| `http` | `content_type` | Response `Content-Type` header. |
| `http` | `body` | Response body, present only when it is valid UTF-8 text. |
| `http` | `body_bytes`, `body_sha256` | Size and SHA-256 of the full body. |
| `http` | `url`, `method`, `artifact_name` | Redacted target URL, method, and artifact name. |

Outputs are published only when the step succeeds. Failed, canceled, timed-out,
and paused steps publish nothing. First-party Open Pilot and Devlog pipelines
keep their existing state handoff and do not use output references. In
repository-evidence mode, Devlog's `collect-history` step publishes
`evidence_*` outputs; see [Devlog repository evidence](devlog-evidence.md).

## Limits and redaction

- `SWITCHYARD_MAX_STEP_OUTPUT_BYTES` bounds each value (default `65536`,
  accepted range `1024`–`1048576`). An oversized value is omitted from the
  persisted outputs, a `step.output.omitted` event names it with its byte count,
  and a downstream reference to it fails clearly instead of receiving truncated
  data. The full payload remains in the step artifact; `*_bytes` and `*_sha256`
  outputs still describe it.
- A step may publish at most 32 outputs.
- Output names that look like credentials are rejected before persistence;
  values are passed through the same embedded-secret redaction as other
  persisted evidence: `Bearer …`, `sk-…`, `token=…`, and similar patterns are
  replaced with the literal redaction marker.
- Both the memory and PostgreSQL stores enforce the name, count, and size rules
  on write (`run_steps.outputs`, migration `029_run_step_outputs.sql`).

## Output contracts

A workflow-definition step may declare `output_shape`. When a run is created,
the shape is copied into the run's immutable pipeline snapshot as
`output_contract: {"version": 1, "shape": …}`. The runner validates the step's
outputs against that snapshot contract before persisting them or letting a
later step consume them.

### Supported schema subset (contract version 1)

The top-level shape describes the map of named outputs, so its `type` must be
`object`. Top-level keywords: `type`, `description`, `properties`, `required`.
Each top-level property name must be a valid output name (see above), and at
most 32 outputs can be declared.

Each property accepts `type`, `description`, and these type-specific keywords:

| Keyword | Allowed on | Meaning |
|---|---|---|
| `properties`, `required` | `object` | Declared child properties; `required` names must be declared. |
| `items` | `array` | Schema for every element. |
| `maxItems` | `array` | Maximum element count (`0`–`1048576`). |
| `maxLength` | `string` | Maximum length in Unicode code points (`0`–`1048576`). |
| `enum` | `string` | Allowed values; at most 64 values of up to 1024 bytes each. |

`type` is one of `string`, `number`, `integer`, `boolean`, `object`, `array`.
Nesting is limited to 8 levels and 256 schema nodes. Every other keyword is
rejected, including `$ref`, `pattern`, `format`, `minLength`, `minimum`,
`additionalProperties`, `oneOf`/`anyOf`/`allOf`, and a `null` type. Every
keyword in the subset is enforced.

Outputs are strings, so the declared type of a top-level property says how its
value is read:

| Declared type | Accepted output value |
|---|---|
| `string` | The raw text. |
| `number` | JSON number text, for example `-1.5e3`. |
| `integer` | JSON number text with an integral value (`2`, `2.0`). |
| `boolean` | Exactly `true` or `false`. |
| `object`, `array` | JSON text of that type, validated recursively. |

Nested values use JSON types directly. Outputs the shape does not declare, and
nested properties it does not declare, pass through unvalidated. Executor
metadata outputs such as `content_bytes` remain available without being
declared.

Validation runs on the bounded, redacted values that downstream steps would
read. A required output that was omitted because it exceeded
`SWITCHYARD_MAX_STEP_OUTPUT_BYTES` is reported as omitted, with its size.

### Author-facing validation

`PUT /api/workflow-definitions/{id}` returns `400` when an `output_shape` falls
outside the subset. The message names the step and the schema location, for
example:

```
invalid input: step "emit" output_shape is not a supported output contract: output_shape.properties.count: unsupported keyword "pattern"
```

Activation and rollback apply the same check, so a version stored before
contracts were enforced, with a shape outside the subset, cannot be activated
or restored. Save a corrected version instead.

### Run-time failures

When outputs violate the contract, the step is recorded as `failed` with
nothing persisted in `outputs`, a `step.output.contract.violated` event is
added, and no later step runs. Each problem names a JSON path:

```
step "produce" output violates its output contract (v1): $.count: expected integer encoded as JSON text, got "two"; $.report.items[0].id: required property is missing
```

At most 10 problems are listed. The failure is deterministic for the recorded
output, so it is classified non-retryable and the run pauses. An operator can
retry the step after fixing the producer.

If a snapshot contract cannot be enforced, the step fails before its executor
runs. This happens with an unknown contract `version`, or with a shape outside
the subset that was stored before enforcement and snapshotted from an
already-active definition. The error ends with
`update the workflow definition output_shape and activate a new version`.

### Compatibility, retries, and rollback

- Steps without `output_shape` have no `output_contract`; their outputs are not
  validated, and their snapshot JSON and `pipeline_version` are unchanged.
- Runs whose snapshot was created before output contracts existed carry no
  `output_contract` and keep executing without validation, even if the current
  definition now declares a shape.
- The snapshot governs. Same-run retries, worker restarts, operator resume
  (`retry_from_run`), and `POST /api/runs/{runId}/retry` reuse the original
  snapshot, so activating or rolling back a definition never changes the
  contract of an existing run. New runs use the active definition.
- Restored outputs are checked against the snapshot contract before any later
  step consumes them. A record that fails (for example one written before
  enforcement) is withheld, and the consuming reference fails with
  `recorded outputs fail the producing step's output contract: …`.
- The contract is part of the snapshot JSON and therefore of
  `pipeline_version`, so resume restores outputs only from a source run that
  executed the same contract.

No database migration is involved; contracts are stored in the existing
`runs.pipeline_snapshot` and `workflow_definitions.pipeline` JSONB columns.

## Durability and restoration

Outputs are keyed by the immutable run step identity (`run_id`, `position`);
the step id does not change across attempts. `StartRunStep`, `RetryRunStep`,
and `RecoverRunStep` clear outputs so only the succeeded attempt's values are
visible.

- **Same-run retry or worker restart:** when a run is requeued after a failure,
  panic, or expired lease, succeeded steps are skipped and their persisted
  outputs are loaded from the run's own step records before any later step
  resolves references.
- **Operator resume (`POST /api/runs/{id}/steps/{step}/retry`):** the new run
  carries `retry_from_run` and `retry_from_step`. Outputs of the source run's
  succeeded steps before the resume position are restored when the source run
  executed the same `pipeline_version`; the run records a
  `step.outputs.restored` event. A version mismatch records a `skipped`
  event and downstream references then fail clearly rather than reading values
  produced by a different pipeline.

Persisted outputs appear in `GET /api/runs/{id}` under each step's `outputs`
map (`RunStep.outputs` in the OpenAPI contract).

## Example: fetch, summarize, attach

```json
{
  "id": "fetch-summarize-attach",
  "name": "Fetch, summarize, attach",
  "status": "active",
  "pipeline": {
    "id": "fetch-summarize-attach",
    "workflow_id": "fetch-summarize-attach",
    "name": "Fetch, summarize, attach",
    "steps": [
      {"id": "fetch", "position": 1, "name": "source.fetch", "executor": "http",
       "inputs": {"allow_http": "true", "url": "https://docs.example.test/changelog.json"}},
      {"id": "summarize", "position": 2, "name": "source.summarize", "executor": "model", "model": "${model}",
       "inputs": {"prompt": "Summarize this changelog for operators:\n\n{{steps.fetch.outputs.body}}"}},
      {"id": "attach", "position": 3, "name": "source.attach", "executor": "internal",
       "inputs": {"artifact_name": "summary.md", "artifact_kind": "markdown",
                  "summary": "Summarized {{steps.fetch.outputs.body_bytes}} bytes from {{steps.fetch.outputs.url}}",
                  "output.summary_text": "{{steps.summarize.outputs.content}}"}}
    ]
  }
}
```

The `fetch` host must be listed in `SWITCHYARD_HTTP_ALLOWED_HOSTS`.
