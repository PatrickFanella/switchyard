# Workflow catalog

The dashboard's **Workflows** view (`#/workflows`) lists stored workflow
definitions, edits linear drafts, and compares, activates, and rolls back
versions. It uses the existing definition API:

| Call | Use |
|---|---|
| `GET /api/workflow-definitions` | Catalog list. |
| `GET /api/workflow-definitions/{id}` | Detail view, and a re-read before every save. |
| `PUT /api/workflow-definitions/{id}` | Save a draft, with `expected_version`. |
| `GET /api/workflow-definitions/{id}/versions` | Version history and comparison. |
| `POST /api/workflow-definitions/{id}/activate` | Activate the latest version, with `{"version": N}`. |
| `POST /api/workflow-definitions/{id}/rollback` | Roll back, with `{"version": V, "expected_version": N}`. |
| `GET /api/agent-profiles` | Profiles offered for each step. |
| `GET /api/control-access` | The caller's role (`admin`, `read`, or `unrestricted`), used to show read-only screens. |

## Routes

| Hash | Screen |
|---|---|
| `#/workflows` | Catalog with search (ID, name, description, step ID/name, executor, profile) and a status filter: definitions with an active version, or whose latest version is a draft. |
| `#/workflows/view/<id>` | Ordered steps of the latest version, inputs, declared outputs, the step outputs each step reads, and the version history. |
| `#/workflows/edit/<id>` | Draft editor for a stored definition. The ID cannot change. |
| `#/workflows/new` | Draft editor for a new definition. |
| `#/workflows/duplicate/<id>` | New draft pre-filled from `<id>` under an unused `<id>-copy[-n]` ID. |

## Editor

- **Metadata:** ID, name, description, owns, uses.
- **Run input fields:** `input_shape` properties with a type and a required flag.
- **Steps:** added, removed, and reordered with Move up/Move down. Positions
  are assigned from the order on save. The executor is one of `internal`,
  `model`, `command`, or `http`. Each executor shows the inputs it reads, and
  any other input can be added by name.
- **Profiles:** the step's profile list contains only profiles whose
  `allowed_executors` include the step executor. The server checks the profile
  and model again on save.
- **Step output bindings:** from the second step onward, a picker lists the
  outputs of earlier steps (outputs declared in `output_shape`, outputs the
  executor publishes, and internal `output.<name>` inputs). It inserts
  `{{steps.<step_id>.outputs.<name>}}` into a chosen input. References to
  unknown or later steps, and malformed references, are flagged before saving.
  See [Step outputs](step-outputs.md).
- **Output contracts:** each declared output has a name, type, description,
  and required flag. It also has `maxLength` and `enum` for strings, and
  `maxItems` for arrays. Keywords without a dedicated control, such as nested
  `properties`/`required` and `items`, are edited as JSON. The server applies the
  contract v1 subset and reports the schema path of any rejected keyword.

Ctrl+S (⌘S on macOS) saves and Escape leaves the editor. In the catalog, the
Down arrow moves from the search box into the results, and the Up and Down
arrows move between entries.

## Validation

The editor itself rejects only drafts it cannot encode: a missing ID, a new ID
that already exists or uses characters other than letters, digits, `.`, `_`,
and `-`, keyword JSON that is not an object, and non-integer limits. All other
rules come from `PUT /api/workflow-definitions/{id}`. Its `400` responses name
the field or step, for example:

```
invalid input: step "write": profile "content-writer" does not allow executor "command"
invalid input: step positions must run 1 to 3 without gaps; position 2 is missing
```

The editor shows these messages verbatim, keeps the edits, and moves focus to
the error.

## Versions and activation

Every save appends an immutable version. A definition has two pointers:

- `version`: the latest saved version, which the editor opens and edits.
- `active_version`: the version new runs snapshot. It is absent when no
  version is active; new runs then use the workflow's built-in pipeline or
  executor.

`PUT` saves a draft and never changes `active_version`. Saving over an active
definition leaves the active version serving new runs until an operator
activates the draft. Before migration `036_workflow_definition_active_version.sql`,
such a save took the definition out of service; the migration sets
`active_version` for definitions that were active and leaves drafts without
one.

The version history on the detail screen lists every version newest first,
labelled `active`, `draft` (saved after the active version and never
activated), or `earlier` (older than the active version). **Compare from** and
**Compare to** choose two versions. The comparison lists changed metadata
(name, description, owns, uses, input fields, pipeline ID, name, and
description) and matches steps by ID in run order, marking each as added,
removed, changed, or unchanged. A changed step lists each changed field
(position, name, executor, profile, model, each input, and the output shape)
with its old and new value. Output shapes are compared without regard to key
order.

**Activate version N…** appears when the latest version is not active. The
confirmation dialog names the workflow, the target version, and the version
active now, and states the consequences: which version new runs will use, that
the previous version stays in the history, and that runs already created keep
their pipeline snapshot. The dialog also runs the activation checks the server
applies (a model step needs a prompt or template, a command step a command,
an http step a URL and `allow_http` set to `"true"`, and every executor must be
supported). If a check fails, the confirm button is disabled. The server repeats
all checks. A `400` is shown with the server's message, and the active version
stays unchanged.

**Roll back to vN…** appears on earlier versions. Rollback appends a copy of
version N as the next version and makes it active. No version is edited or
deleted. If the latest version is an unactivated draft, it stays saved but is
no longer the latest, and the dialog says so. Runs already created, including
queued and running ones, keep their snapshot; this is covered by
`TestWorkflowDefinitionVersionContractMemory` and
`TestPostgresWorkflowDefinitionVersionContract`.

## Concurrency and access

Writes carry an optimistic-concurrency precondition, and a failed precondition
returns `409` without changing anything:

| Request | Precondition |
|---|---|
| `PUT` with `"expected_version": 0` | The ID must not exist (create only). |
| `PUT` with `"expected_version": N` | N must still be the latest version. |
| `POST .../activate` with `{"version": N}` | N must still be the latest version. |
| `POST .../rollback` with `"expected_version": N` | N must still be the latest version. |

Requests without these fields keep the earlier unconditional behaviour. The
editor sends `expected_version` on every save. It also re-reads the definition
first, so the common case gets a message with both version numbers. A `409`
from the server is shown with a **Reload latest version** action and the edits
are kept. The version history reports an activation or rollback conflict and
offers **Reload workflow**.

Activation and rollback need the admin role, as all control-plane writes do. A
read-role credential sees the catalog, detail views, version history, and
comparison, but no authoring, activation, or rollback actions, and the editor
is disabled. If `/api/control-access` is unavailable, the view assumes it can
write and switches to read-only when a save, activation, or rollback returns
`401` or `403`.

Unsaved edits are guarded. Leaving through the sidebar, browser back/forward,
Cancel, or Escape asks for confirmation, and closing or reloading the tab
triggers the browser's unsaved-changes prompt.
