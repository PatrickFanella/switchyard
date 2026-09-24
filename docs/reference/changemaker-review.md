# Changemaker review, revision, and publication

A grounded Changemaker run produces a draft, `changemaker-digest.md`, and the
SHA-256 of the source evidence it was drafted from (`changemaker-sources.json`;
see [Source collection](source-collection.md)). Review records decisions
against those two exact digests. A revision request creates a new draft
version and keeps the earlier ones. Publication is off by default, and no run
publishes by itself: the pipeline has no publication step, and publication is
only an admin request on the control listener.

## Threads and versions

The run that produced the first draft is version 1, and its run ID is the
thread ID. Each revision request appends a version backed by a new run.
Versions record the parent version, the run whose `collect-sources` step
produced the evidence (`sources_run_id`), the source mode, the revision notes,
and the requesting actor. Version 1 is stored when the first revision is
requested; until then the thread is the run alone.

The **current version** is the highest version whose run has not failed or
been canceled. Only the current version can be decided on, revised, or
published. Earlier versions are `superseded`. When a revision run fails, the
version it revised becomes current again. A thread holds at most 10 versions.

## Review

`GET /api/changemaker/drafts/{run_id}` (control listener, read role) accepts the
run ID of any version and returns the whole thread:

- every version with its run status, the `changemaker-digest.md` draft (inline,
  for preview), `draft_sha256`, `evidence_sha256`, the evidence artifact ID on
  the sources run, grounding status, cited and unmatched source IDs, source
  mode, revision notes, and `state`;
- all decisions and publication receipts of the thread;
- `publication`: whether publication is enabled, the adapter, the destination,
  the reason when disabled, the approval lifetime, the version limit, and the
  policy text.

A version is `not-ready` until its run has succeeded with a draft artifact and
an evidence digest; a run without a source evidence digest (for example the
legacy prompt-only pipeline) cannot be reviewed. When the attached evidence
digest does not match `changemaker-sources.json`, the version is also
`not-ready`. Other states: `no-draft` (no sources were collected, so the
model was not called), `awaiting-review`, `approved`, `rejected`,
`revision-requested`, `approval-expired`, `destination-changed`, `publishing`,
`publication-failed`, `published`, and `superseded`.

The run cockpit shows this as the Changemaker review panel for
`changemaker-daily-digest` runs: revision history, draft preview with both
digests, decision and revision controls, publication controls, receipts, and
the decision log.

## Decisions

`POST /api/changemaker/drafts/{run_id}/decisions` (admin role):

```json
{"version": 2, "draft_sha256": "<64 hex>", "evidence_sha256": "<64 hex>", "decision": "approved", "notes": "reviewed"}
```

- `version`, `draft_sha256`, and `evidence_sha256` must equal the values of the
  run's version (409 `version-mismatch`, `stale-draft`, `stale-evidence`), and
  that version must be current (409 `superseded`) and ready (409
  `draft-not-ready`). A published thread accepts no further decisions (409
  `already-published`).
- `approved` requires a generated draft (409 `draft-not-generated`).
- The decision records the actor, notes, draft artifact ID, both digests, and
  the publication adapter and destination configured at that moment
  (`disabled` and an empty destination when publication is off). Approvals
  expire after 24 hours.
- The newest decision on a version's exact digests is its current decision. A
  later rejection replaces an earlier approval.

## Revisions

`POST /api/changemaker/drafts/{run_id}/revisions` (admin role):

```json
{"version": 1, "draft_sha256": "<64 hex>", "evidence_sha256": "<64 hex>", "notes": "Name the input that crashed.", "sources": "same"}
```

The same preconditions as decisions apply, and `notes` is required (at most
2,000 bytes). The request records a `revision-requested` decision bound to the
revised version's digests and creates a run for the next version from the same
pipeline snapshot, so a workflow activation or rollback in the meantime does
not change the thread. The new run's `prompt` is the thread's original writer
prompt followed by the reviewer notes and the previous draft, each in its own
delimited block with template braces and closing tags neutralized. The run has
no trigger ID.

- `sources: "same"` (default) resumes the new run at the drafting step with the
  `collect-sources` outputs restored from the run that collected them. No
  source is queried and the evidence digest is unchanged. It requires that run
  to still exist with the same pipeline version (409 `sources-unavailable`
  otherwise).
- `sources: "recollect"` runs the whole pipeline again with `source_until` and
  `source_window` pinned to the original collection window. The new run
  records whatever evidence digest results; it equals the original when the
  sources did not change.

The version and decision are written in one store transaction that fails with
409 when another revision was recorded first; the new run is then canceled.
With synchronous dispatch the response is 201 after the run finishes; with
asynchronous dispatch it is 202 with a `Location` header.

## Configuration

| Setting | Meaning |
|---|---|
| `CHANGEMAKER_PUBLICATION_ADAPTER` | `disabled` (default) or `gitea-issue-comment`. |
| `CHANGEMAKER_PUBLICATION_GITEA_ISSUE` | The one destination, `owner/repo#number`. |
| `CHANGEMAKER_PUBLICATION_ALLOWED_REPOS` | Comma-separated `owner/repo` slugs that may receive publications. The destination repository must be listed. |

The adapter also needs `GITEA_BASE_URL` and `GITEA_TOKEN`. If any value is
missing or inconsistent, publication stays disabled and the review reports
why. Requests cannot choose a destination. An approval authorizes only the
adapter and destination recorded with it, so an approval made while
publication was disabled, or before the destination changed, cannot publish.
Settings are file-managed and need an API restart.

## Publication

`POST /api/changemaker/drafts/{run_id}/publications` (admin role):

```json
{"approval_id": "changemaker-decision-…", "draft_sha256": "<64 hex>"}
```

The idempotency key is the SHA-256 of the adapter, destination, and draft
digest. Each request:

1. Refuses with 409 when publication is disabled (`publication-disabled`), the
   draft is not ready, or `draft_sha256` is not the current draft digest
   (`stale-draft`).
2. Returns `already-published` (200) without contacting the destination when a
   published receipt exists for the key.
3. Refuses with 409 when another version of the thread was published
   (`already-published`), the version is not current (`superseded`), no draft
   was generated, or the approval is missing (`approval-required`), not an
   approval (`approval-rejected`), bound to other draft or evidence digests
   (`stale-approval`), bound to another destination (`destination-changed`),
   expired (`approval-expired`), or replaced by a later decision
   (`approval-superseded`).
4. Claims the receipt. A concurrent attempt gets 409
   `publication-in-progress`; a claim older than two minutes can be taken over.
5. Asks the adapter whether the destination already holds the key's marker.
   If so, the receipt is marked delivered and `reconciled` (200) and nothing is
   posted. If the destination cannot be searched, the attempt fails without
   posting.
6. Posts the draft and records `published` (201), or records `failed` with a
   redacted error (502). Repeating the same request retries.

The posted body is the exact `changemaker-digest.md` bytes, a footer naming the
run, version, thread, draft digest, and evidence digest, and the hidden marker
`<!-- switchyard-changemaker-publication:<key> -->`. Bodies above 60,000 bytes
are refused (409 `draft-too-large`).

The Gitea issue comment adapter is the one used for
[Devlog delivery](devlog-delivery.md), with its own marker prefix: it trusts a
marker only in comments written by the account behind `GITEA_TOKEN`, and it
reports an error instead of posting when the issue has more comments than its
scan covers.

## Storage

Versions, decisions, and receipts are stored in `changemaker_draft_versions`,
`changemaker_review_decisions`, and `changemaker_publication_receipts`
(migration `038_changemaker_review.sql`). They reference runs and artifacts by
ID without foreign keys, so review and publication evidence survives run
retention. Receipts share the claim contract and code with Devlog delivery
receipts but live in their own table. The memory store implements the same
contract.
