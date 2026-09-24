# Workflow catalog

The dashboard's **Workflows** view (`#/workflows`) lists stored workflow
definitions and edits linear drafts. It uses the existing definition API:

| Call | Use |
|---|---|
| `GET /api/workflow-definitions` | Catalog list. |
| `GET /api/workflow-definitions/{id}` | Detail view, and a re-read before every save. |
| `PUT /api/workflow-definitions/{id}` | Save a draft. |
| `GET /api/agent-profiles` | Profiles offered for each step. |
| `GET /api/control-access` | The caller's role (`admin`, `read`, or `unrestricted`), used to show read-only screens. |

The view does not activate or roll back definitions.

## Routes

| Hash | Screen |
|---|---|
| `#/workflows` | Catalog with search (ID, name, description, step ID/name, executor, profile) and a status filter. |
| `#/workflows/view/<id>` | Ordered steps, inputs, declared outputs, and the step outputs each step reads. |
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

## Saving is not activation

`PUT` stores a new draft version and makes it the current definition. A saved
draft never serves runs until it is activated separately.

Saving a draft over an **active** definition has a consequence the editor
states and requires the operator to acknowledge. The stored definition's
current status becomes `draft`, so until a draft is activated again new runs
of that workflow do not use a stored definition. They fall back to the
workflow's built-in pipeline or executor. Runs created earlier keep their
pipeline snapshot.

## Concurrency and access

The definition API has no create-only or version precondition. Before each
save the editor re-reads the definition. It refuses to create over an existing
ID, and it refuses to save an edit whose stored version changed since the
editor opened. The re-read narrows the race with another writer but does not
close it.

A read-role credential sees the catalog and detail views without authoring
actions, and the editor is disabled. If `/api/control-access` is unavailable,
the editor assumes it can write and switches to read-only when a save returns
`401` or `403`.

Unsaved edits are guarded. Leaving through the sidebar, browser back/forward,
Cancel, or Escape asks for confirmation, and closing or reloading the tab
triggers the browser's unsaved-changes prompt.
