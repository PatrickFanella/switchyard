# Agent profiles

Agent profiles are Switchyard-native policy for pipeline steps: the executors a step may use, the models it may select, a default model, and the prompt contract prepended to model requests. Blacktower-style personas live here; there is no separate application boundary.

Source: `backend/internal/controlplane/profiles.go`, `store_profiles.go`, `postgres_store_profiles.go`, `http_agent_profiles.go`, migration `030_agent_profile_versions.sql`. Route contract: `docs/openapi.yaml`.

## Identity and versions

- A profile ID is stable text: 1 to 64 lowercase letters, digits, or hyphens, not starting with a hyphen (the `AgentProfileID` pattern in `docs/openapi.yaml`).
- Shipped static defaults (`content-writer`, `devlog-summarizer`, `open-pilot-executor`, `operator-reviewer`) are compiled into the binary. They report `version` 0, `status` `static`, `source` `static`. They are never written to the database and never change at runtime.
- Persisted versions are immutable bodies numbered from 1 per profile ID. Each version has a lifecycle status: `draft` (saved, not serving), `active` (serving), `superseded` (was active or draft, replaced). Bodies are never rewritten; only the status, `updated_at`, and `activated_at` change.
- The identity record tracks `active_version`, `draft_version`, and `latest_version`. `active_version` 0 means the static default (if one exists) serves requests.
- Every version carries `prompt_contract_sha256` and a `provenance` object: `origin` (`static-default` when derived while the static default served, `operator`, or `rollback`), `actor`, `note`, `derived_from_version`, `rolled_back_from_version`, and `static_contract_sha256` (the shipped default's contract hash at write time, when a default exists).

## Effective resolution

`StoreBackedAgentProfileRegistry` resolves an ID to the active persisted version first and otherwise to the static default. Existing pipeline and workflow-definition references keep working unchanged: nothing is migrated into the tables until an operator saves a draft. Migration `029` creates empty tables only.

Workflow-definition writes validate profile references against the effective registry, so a persisted profile can be referenced by definitions and a persisted narrowing is enforced for new definitions.

## Run and step snapshots

When a run is created, the effective policy for every profile referenced by its pipeline is copied into `pipeline_snapshot.profiles` (keyed by profile ID). The runner reads the step's profile from that snapshot before consulting the registry, so activation or rollback after run creation never changes an in-flight, paused, retried, or CI-repair child run. Runs created before this snapshot existed have no `profiles` map and resolve through the registry, which still serves the static defaults.

`pipeline_version` hashes the pipeline shape only; the profile snapshot is excluded so runs of one pipeline share a version regardless of which profile version applied.

Each step that uses a profile records correlation IDs: `agent_profile_id`, `agent_profile_role`, `agent_profile_contract_sha256`, `agent_profile_model`, `agent_profile_version`, `agent_profile_source`, `agent_profile_model_source` when a model was resolved, and `agent_profile_origin` when provenance exists. The `agent-profile.resolved` run event is unchanged.

## Control API

All routes are on the control listener only; the model listener returns 404 for `/api/agent-profiles*`. Reads need the read or admin role. `PUT` and `POST` need the admin role (token or authenticated reverse-proxy identity); the acting identity is stored on every audit entry and version provenance.

| Method and path | Purpose |
|---|---|
| `GET /api/agent-profiles` | Effective profiles (`profiles`) and persisted identities (`identities`) |
| `GET /api/agent-profiles/{id}` | Effective `profile` plus `identity` (synthesized with `static_default: true` for static-only IDs) |
| `PUT /api/agent-profiles/{id}` | Save a new draft version; body `AgentProfileDraftRequest` |
| `GET /api/agent-profiles/{id}/versions` | Immutable version history, newest first |
| `GET /api/agent-profiles/{id}/audit` | Append-only audit entries, newest first, `limit` up to 100 |
| `POST /api/agent-profiles/{id}/activate` | Activate a draft; body `{ "version": n, "reason": "..." }` |
| `POST /api/agent-profiles/{id}/rollback` | Restore a previously active version as a new active version |

### Validation

Draft bodies are rejected with 400 when: the ID is malformed; `name`, `role`, or `prompt_contract` is empty or over its limit (128, 128, 16384); `allowed_executors` is empty or names an executor outside `internal`, `model`, `opencode`, `command`, `http`, `git`, `gitea`; a model name is empty, contains whitespace, is `${model}`, or exceeds 128 characters; or `default_model` is set while `allowed_models` is non-empty and does not contain it. Unknown JSON fields are rejected.

Activation and rollback are rejected with 400 when the target body would stop allowing an executor or fixed model that a shipped default pipeline or an active workflow definition uses with that profile. This prevents an activation from breaking `devlog`, `open-pilot`, or operator-defined pipelines.

### Concurrency and conflicts

- `PUT` requires `expected_version` equal to the identity's `latest_version` (0 before the first save). A mismatch returns 409 and writes nothing. Two concurrent saves with the same `expected_version` produce exactly one new version.
- `activate` requires the target to be a `draft`; activating an `active` or `superseded` version returns 409.
- `rollback` requires a target that was active at some point (400 otherwise) and is not currently active (409).

### Audit

Every save, activation, and rollback appends an entry with `action` (`draft-saved`, `activated`, `rolled-back`), `actor`, `reason`, `version`, `previous_active_version`, and `prompt_contract_sha256`. Entries are never updated or deleted by the application. In PostgreSQL they live in `agent_profile_audit`; the memory store mirrors the contract for local runs.

## Storage

PostgreSQL tables from migration `030_agent_profile_versions.sql`:

- `agent_profiles` — identity: `active_version`, `draft_version`, `latest_version`, `static_default`, timestamps.
- `agent_profile_versions` — immutable bodies keyed by `(profile_id, version)`, a partial unique index guaranteeing at most one `active` row per profile, JSON `allowed_models`, `allowed_executors`, and `provenance`.
- `agent_profile_audit` — append-only ledger ordered by `id`.

Identity rows are locked `for update` during save, activate, and rollback so version numbers and status transitions are serialized per profile.

## Per-run model overrides

A non-empty `inputs.model` on `POST /api/runs` is a run-level model override. Source: `run_model_policy.go`.

A step accepts the override when its model is not fixed by the workflow: a profile-bound step with no `model`, or a step whose `model` is `${model}`. The allowed overrides for a step are the profile's `allowed_models` (empty means any) intersected with the workflow executor's allowlist when one exists. Devlog and Open Pilot `${model}` steps use `DEVLOG_ALLOWED_MODELS` and `OPEN_PILOT_ALLOWED_MODELS`. Generic workflows add no executor allowlist.

For workflows with a pipeline, the runner checks the override against the new run's profile snapshot before the run is created. It rejects the override with 400 and creates nothing when:

- the value is not a single token of at most 128 characters, or is `${model}`;
- no step of the workflow accepts an override;
- a step that accepts it would reject it, for example `step "devlog.generate-markdown": agent profile "devlog-summarizer" does not allow model "m3"; allowed: m2`.

The message has the form `invalid input: model override "<model>" rejected: <reasons>`. Runs without a pipeline, Open Pilot lifecycle resumes, and webhook admissions are not checked at dispatch; the runner still enforces the profile allowlist when each step runs, as before. Retries reuse the original snapshot, so they are checked against the profile versions the original run captured.

`GET /api/workflows/{id}/model-policy[?model=<name>]` previews this without creating a run. It needs the read or admin role and is served only on the control listener. It returns 404 for an unknown workflow or one without a pipeline.

## Resolved policy in run evidence

`GET /api/runs/{id}` includes `policy` (`RunModelPolicy`) for runs with a pipeline snapshot. The same type is returned by the preview route. For each step it reports:

- `profile_id`, `profile_version`, `profile_source` (`static` or `persisted`), and `profile_captured` (`run-snapshot`, or `registry` for runs created before profile snapshots existed). The profile always comes from the workflow step.
- `model` and `model_source`: `workflow` (fixed by the step), `override` (the run's `model` input), `profile-default`, `executor-default` (the workflow's configured default, such as `DEVLOG_MODEL`), `none`, or `unrecorded` (a step recorded before model sources existed whose model differs from the current prediction).
- `inherited_model` and `inherited_model_source`: what the step uses without an override.
- `accepts_override`, `override_restricted`, and `allowed_overrides`.
- `recorded`: true once the step has run. Recorded steps report the values the runner stored in their correlation IDs, which now include `agent_profile_model_source`.

The run-level fields are `override`, `override_status` (`none`, `accepted`, `rejected`), `rejections`, and the intersection of `allowed_overrides` across steps. Because the policy is read from the run's snapshot, activating or rolling back a profile after the run was created does not change it.

## Operator UI

- **Agents** view (`#/agents`, `ProfilesPage.tsx`, client in `web/src/profileVersions.ts`): lists effective profiles and draft-only identities. It shows the serving version, default and allowed models, executors, contract hash, identity counters, version history with provenance, and the audit log. Admin callers can edit a new draft from the current draft or serving version, activate the pending draft, and roll back to any previously active version, each with an optional audited reason. Saving sends `expected_version` equal to the identity's `latest_version`. On a 409 the editor keeps the edits, explains the conflict, and offers to reload the latest version before saving again. Read-role callers see the same data without editing controls; a 401 or 403 on a mutation switches the page to read-only. Unsaved drafts are guarded like workflow drafts.
- **Overview → Dispatch with model override** (`RunModelOverrideDispatch.tsx`): picks an enabled non-Open-Pilot trigger, loads the model-policy preview, and explains each step's inherited model and override rule. The override field is a list of the allowed models when the policy restricts them. The server's rejection message is shown verbatim.
- **Run cockpit → Resolved policy** (`RunPolicyEvidence.tsx`): renders the run's `policy`, with each step's profile version and the source of its model.

Workflow authors choose a profile per step in the workflow draft editor, which offers only profiles that allow the step's executor (see [Workflow catalog](workflow-catalog.md)).
