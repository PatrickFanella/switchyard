# Source collection and grounded drafting

A workflow step with executor `source` reads bounded source material through a
named adapter and publishes it as [step outputs](step-outputs.md). A later
model step receives that material in its prompt, so a draft is written from
sources the run collected and recorded, not from knowledge the model is assumed
to have. The shipped Changemaker daily digest uses this pattern.

Collection is read-only. No adapter creates, edits, or comments on anything in
the source system, and no step in the Changemaker pipeline publishes the draft.

## First adapter: `gitea-activity`

`gitea-activity` lists issues and pull requests updated inside a time window in
one or more Gitea repositories (`GET /api/v1/repos/{owner}/{repo}/issues` with
`state=all`, `since`, and `before`).

It was chosen because Switchyard already holds the credential and client for
it: `GITEA_BASE_URL` and `GITEA_TOKEN`, used by Open Pilot. It needs no new
secret and no new outbound host, and its data has natural references (issue
and pull request URLs) and a natural time window. The generic HTTP executor
was the alternative. It fetches one arbitrary URL per step and has no notion
of items, windows, or deduplication.

Access is opt-in twice:

1. The host operator lists readable repositories in
   `SWITCHYARD_SOURCE_REPOSITORIES` (comma-separated `OWNER/REPO`). An empty
   value means no repository can be collected. The token may be able to read
   more repositories; the allowlist limits collection to the listed ones.
2. The run names the repositories it wants. Without them, nothing is collected.

A requested repository that is not allowlisted is recorded as a failed source
and is never requested from Gitea.

## Step inputs

| Input | Required | Meaning |
|---|---|---|
| `adapter` | yes | `gitea-activity`. Definitions naming any other adapter are rejected on save. |
| `repositories` | no | Comma- or space-separated `OWNER/REPO` list. Duplicates are removed case-insensitively and the list is sorted. At most 10. |
| `window` | no | Go duration from `1h` to `720h`. Default `24h`. |
| `until` | no | RFC 3339 end of the window. Default: the run's creation time, truncated to the second. Must not be later than the run's creation time. |
| `max_items` | no | Maximum items kept, 1–100. Default 50. |
| `max_excerpt_chars` | no | Maximum body excerpt per item in characters, 0–2000. Default 600. |
| `artifact_name` | no | Evidence artifact name. Default `<step_id>-evidence.json`. |

An input left as an unrendered run-input template, such as
`{{source_repositories}}` when the run did not supply it, counts as unset.
Invalid values fail the step as a non-retryable error, which pauses the run.
Source steps cannot carry an agent profile.

## Determinism

The window is anchored to the run's creation time or to an explicit `until`,
so a same-run retry and a worker restart read the same window. Items are:

1. filtered to `updated_at` inside the window (items outside are recorded as
   omitted);
2. deduplicated by repository and number, keeping the latest update;
3. capped at `max_items`, keeping the most recently updated (ties by
   repository, then number);
4. ordered by repository, then number, and numbered `S1`, `S2`, … in that
   order;
5. cut at the first item that would push `content`, `references`, or `items`
   past the output budget (`SWITCHYARD_MAX_STEP_OUTPUT_BYTES` minus 4096
   bytes, or half the limit, whichever is larger), so no output is dropped by
   the step output limit.

Every item dropped by steps 1, 3, or 5 appears in the evidence artifact's
`omitted_items` with the reason. Each repository is read for at most 5 pages
of 50; a repository with more activity is flagged `truncated` in the evidence
and noted in `references`.

Collecting unchanged upstream data twice over the same window yields the same
outputs and the same `evidence_sha256`, regardless of the order Gitea returns
items.

## Outcomes

| Situation | Step result | Status output |
|---|---|---|
| Every requested source succeeded and at least one item was kept | succeeded | `complete` |
| Some sources failed, at least one item was kept | succeeded | `partial` |
| No items were kept, including when no sources were requested or all successful sources had no activity | succeeded | `empty` |
| At least one source was requested and every one failed | failed | none |

When every requested source fails, the step fails and no draft is produced.
The error is retryable when at least one failure was transient (network
error, HTTP 5xx, or 429). Otherwise (not allowlisted, `GITEA_BASE_URL` unset,
HTTP 4xx) the run pauses. The evidence artifact and a
`source.collection.unavailable` event are recorded even though the step
failed. Each failed source also adds a `source.collection.failed` event.

## Outputs

| Output | Value |
|---|---|
| `adapter` | `gitea-activity` |
| `status` | `complete`, `partial`, or `empty` |
| `item_count`, `source_count`, `failed_source_count`, `omitted_item_count` | Integers |
| `window_start`, `window_end` | RFC 3339 UTC |
| `content` | Item blocks for the model: `[S1] owner/repo#12 (pull request, merged)`, title, URL, author and timestamps, labels, and a quoted body excerpt. `No source items were collected.` when empty. |
| `references` | Markdown source list rendered by Switchyard, one line per item with its ID and link; `No sources collected.` when empty, plus notes about truncation or omissions. |
| `failures` | Markdown list of `owner/repo: reason`, or `none` |
| `source_ids` | `S1,S2,…`; empty when nothing was collected |
| `items` | JSON array of `{id, source, kind, number, title, state, url, updated_at}` |
| `evidence_sha256` | SHA-256 of the evidence artifact bytes; equals the artifact's recorded `sha256` |
| `artifact_name` | Evidence artifact name |

Collected text cannot close the `<collected-sources>` delimiter used in the
drafting prompt: the tags are replaced inside titles and excerpts.

The evidence artifact (kind `source-evidence`, schema
`switchyard.source-evidence.v1`) records the adapter and version, window,
limits, per-source status and error, each kept item with its excerpt,
`body_bytes`, and `body_sha256`, and every omitted item.

## Grounded model steps

A generic `model` step becomes grounded when it declares
`grounding_item_count`, normally bound to
`{{steps.<source_step>.outputs.item_count}}`:

- With `0`, the model is not called. The step succeeds with `content` set to
  `No sources collected. The model was not called, so this run has no
  generated draft.`, publishes `generated=false`, and records a
  `model.generation.skipped` event.
- Otherwise the model is called. The response is scanned for citations such as
  `[S1]` or `[S2, S3]`, checked against `grounding_source_ids` (bind it to
  `source_ids`), and published as `cited_sources` (collected IDs the draft
  cites) and `unknown_citations` (cited IDs that match no collected item).
  Both are comma-separated and empty when there are none.

A value that is not a non-negative integer fails the step as non-retryable.
Grounded steps also publish `generated` and `grounding_item_count`.

## Changemaker daily digest

`changemaker-daily-digest` is a four-step linear pipeline:

| Step | Executor | Purpose |
|---|---|---|
| `prepare` | internal | Records prompt inputs (`changemaker-input.json`), unchanged from version 1. |
| `collect-sources` | source | `gitea-activity` over `source_repositories`, `source_window`, `source_until`, `source_max_items`; attaches `changemaker-sources.json`. |
| `changemaker-draft` | model (`content-writer`) | Grounded on `collect-sources`. The prompt tells the model to write only from the items between `<collected-sources>` tags, cite each factual statement by ID, leave out unsupported claims, and keep suggestions separate. Attaches `changemaker-draft.md`. |
| `attach` | internal | Attaches `changemaker-digest.md` and publishes the handoff outputs. |

Run inputs: `topic` and `prompt` (required), and the optional
`source_repositories`, `source_window`, `source_until`, and
`source_max_items`.

`changemaker-digest.md` keeps generated text and sources apart. Its
"Generated draft" section is the model output. "Citation check" lists cited
and unknown IDs. "Collected sources" is written by the adapter, not the model,
and gives the collection status, window, counts, evidence artifact digest,
reference list, and failures.

Each step declares a v1 output contract (`output_shape`), so a producer that
stops publishing a declared output fails before a later step reads it. The
`attach` step publishes `grounding_status`, `draft_generated`,
`generated_content_sha256`, `evidence_sha256`, and `artifact_name` for review
and publication workflows to bind to.

The internal `attach` step uses `artifact_body`: when an internal step names an
`artifact_name` and sets `artifact_body`, the rendered body is the artifact
content in place of the rendered-inputs dump.

### Upgrading from version 1

Version 1 was prompt-only (`prepare`, `changemaker-draft`, `attach`). At
startup, `SeedDefaults` saves the grounded pipeline as a new active version
only when the stored definition is still the unmodified shipped version 1
(version number 1, active, identical pipeline). Runs created earlier keep
their pipeline snapshot and finish on version 1. A definition an operator has
edited or rolled back has a higher version number and is left as it is.

To return to the prompt-only pipeline, roll back:

```bash
curl -X POST .../api/workflow-definitions/changemaker-daily-digest/rollback \
  -H 'Content-Type: application/json' -d '{"version": 1}'
```

The rollback creates a new version, which later startups do not replace.

After the upgrade, a run without `source_repositories` records the no-sources
outcome and produces no generated draft. The seeded
`daily-changemaker-native` cron trigger (disabled by default) supplies only
`topic` and `prompt`, and the trigger API can only enable or disable it, so
enabling it as shipped produces scheduled no-sources outcomes. Scheduled
grounded drafts need a trigger whose inputs include `source_repositories`;
this change does not add one.
