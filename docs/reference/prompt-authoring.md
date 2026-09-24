# Prompt authoring

The dashboard's **Prompts** view (`#/prompts`) edits, previews, and versions
the prompts that model steps send. It does not add a prompt store. Every prompt
has one owner, and the page reads and writes through that owner's existing
versioned routes.

Source: `backend/internal/controlplane/prompt_authoring.go`,
`run_model_policy.go`, `pipeline.go`; `web/src/promptAuthoring.ts`,
`web/src/components/PromptsPage.tsx`, `RunPromptEvidence.tsx`. Route contract:
`docs/openapi.yaml`.

## Ownership

| Prompt | Owner | Versioned by | Edited in |
|---|---|---|---|
| Step template: the `prompt` input of a `model` step, or `template` when `prompt` is empty | The workflow definition | Workflow-definition versions and `active_version` ([Workflow catalog](workflow-catalog.md)) | Prompts view, or the workflow draft editor |
| Prompt contract: the `prompt_contract` of an agent profile | The agent profile | Agent-profile versions ([Agent profiles](agent-profiles.md)) | Agents view |
| Built-in prompt: text the Devlog and Open Pilot executors assemble in Go | The code | Releases | A code change |

Rules that keep one copy of each prompt:

- A step template holds the step's task. The profile contract holds the role
  and standing rules for every step that uses the profile. The runner joins
  them as `Profile contract:\n<contract>\n\nWorkflow request:\n<template>`, so
  a template must not repeat its profile's contract. The inventory and preview
  report a template that contains the contract text (16 characters or longer)
  as an error.
- `GET /api/prompts` is derived on every request from the stored workflow
  definitions, the effective profile registry, and the built-in pipelines. It
  stores nothing.
- Saving a template edit writes a new workflow-definition version through
  `PUT /api/workflow-definitions/{id}`. Profile contracts are edited only on
  the Agents page; the Prompts view shows them read-only with a link.

## Variables and validation

A template reads two kinds of variables, matching the runner:

- `{{name}}`: a run input. The generic executor replaces the exact text
  `{{name}}`, so `{{ name }}` with spaces is never substituted and is reported
  as an error. A name missing from the workflow `input_shape` is a warning,
  because a run that omits it sends the literal token to the model.
- `{{steps.<step_id>.outputs.<name>}}`: an earlier step's output
  ([Step outputs](step-outputs.md)). A malformed reference, a reference to the
  step itself, to an unknown step, or to a step that does not run earlier is an
  error.

Other checks: a model step without a prompt or template (error), a profile
that is unknown or does not allow the step's executor or fixed model (error),
and credential-shaped text in the template (warning). Each template in the
inventory carries its `variables` and `problems`.

## Preview

`POST /api/prompts/preview` renders one model step with sample values:

```json
{
  "workflow_id": "digest",
  "step_id": "write",
  "template": "Summarize {{topic}}:\n{{steps.fetch.outputs.body}}",
  "inputs": { "topic": "Switchyard" },
  "step_outputs": { "fetch": { "body": "Release notes" } }
}
```

`template` is optional unsaved draft text; without it the stored template of
`version` (default: the latest) is used. The server renders in the runner's
order: step output references, then run inputs, then the profile contract
prefix. `TestPromptAuthoringContractMemory` checks that the preview equals the
prompt the runner sends to the model for the same values.

The response lists `missing_variables` with a message for each variable that
has no sample, for example
`missing variable {{topic}}: no sample run input supplied; a run without it sends the literal token to the model`.
Unfilled tokens stay visible in the rendered text. `ok` is false when a
variable is missing or a problem has severity `error`.

Redaction: a sample input whose name looks like a credential (`token`,
`password`, `secret`, `api_key`, ...) is replaced by `[REDACTED]`, and the
rendered text, the template in the inventory, and the profile contract pass
through the same redaction used for other control-plane evidence. `redacted`
reports whether anything was replaced.

The preview is a dry run. It calls no model, creates no run, and stores
nothing.

## Access

Both routes are served on the control listener only; the model listener returns
404. `GET /api/prompts` needs the read or admin role. The preview is a `POST`
because draft text and samples can be large, so like every other non-GET
control route it needs the admin role. This matches
`POST /api/open-pilot/readiness/validate`.

A read-role caller can list templates, variables, problems, profile
constraints, and version history. The page hides preview, save, activation,
and rollback, and switches to read-only when any of them returns 401 or 403.

Permission to edit a prompt does not grant permission to run or publish it.
Saving creates a draft that no run uses until an admin activates it, and the
Prompts view has no dispatch or delivery action.

## Promotion, rollback, and run binding

A template is promoted by activating its workflow-definition version and
rolled back with workflow rollback; the Prompts view embeds the same version
panel as the Workflows view. Profile contracts are promoted and rolled back on
the Agents page.

A run records the prompt versions it uses in its pipeline snapshot:

- `definition_id` and `definition_version` name the workflow-definition
  version whose templates the run executes. Built-in pipelines, and snapshots
  created before this field existed, report `definition_version` 0.
- The profile snapshot records the profile version and contract hash (see
  [Agent profiles](agent-profiles.md)).

Neither field is part of `pipeline_version`, which still hashes only the
pipeline shape. Activation or rollback after a run is created never changes
that run.

`GET /api/runs/{id}` returns them in `policy`: the run-level
`definition_id`/`definition_version`, and for each step `prompt_owner`
(`workflow-definition` or `built-in`), `prompt_input`, and
`prompt_template_sha256`, the SHA-256 of the unrendered template in the
snapshot. The run cockpit's **Resolved policy** section shows them under
**Prompt versions** next to each step's profile contract version and hash.

## Inventory fields

`GET /api/prompts` returns:

- `templates`: one entry per model step in the latest version of each
  workflow definition, with `version`, `active_version`, the redacted
  `template`, `template_sha256`, `active_template_sha256`,
  `changed_since_active`, the step's `profile_id` and `model`, `variables`,
  and `problems`.
- `contracts`: every effective profile with its version, source, models,
  executors, redacted contract, contract hash, and `referenced_by` (the
  workflow steps that use it).
- `built_in`: model and OpenCode steps of built-in pipelines whose prompt is
  assembled in code.
