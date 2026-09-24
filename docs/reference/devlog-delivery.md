# Devlog review, export, and delivery

A finished Devlog run can be reviewed, exported, and, when an operator has
configured a destination, delivered once after an admin approves the exact
report version. Delivery is off by default. Export never contacts a
destination.

## Review

`GET /api/devlog/reports/{run_id}` (control listener, read role) returns:

- the run, its `devlog.md` report artifact, and its `devlog-evidence.json`
  artifact when repository evidence was collected;
- `source`: the source window. In repository-evidence mode it comes from
  `devlog-evidence.json` (window, repositories, item count, completeness,
  missing sources). In run-history mode it states that the report covers
  recent Switchyard runs recorded before the run's creation time;
- `exports`: one link per exportable artifact with its SHA-256 and size;
- `delivery`: whether delivery is enabled, the adapter, the destination, the
  reason when disabled, the approval lifetime, and the policy text;
- the latest decision and the receipt for the current report digest, plus the
  run's approvals and receipts;
- `state`: `not-ready`, `delivery-disabled`, `awaiting-approval`, `approved`,
  `rejected`, `approval-expired`, `delivering`, `delivered`, or
  `delivery-failed`.

The run cockpit shows this as the Devlog report review panel for Devlog runs:
source window, Markdown preview, export buttons, approval controls, and
receipts.

## Export

`GET /api/devlog/reports/{run_id}/export/markdown` returns the exact bytes of
`devlog.md`; `.../export/json` returns the exact bytes of
`devlog-evidence.json` (404 without repository evidence). The response carries
`X-Switchyard-Artifact-SHA256` and `Content-Disposition: attachment`. The API
refuses with 409 `digest-mismatch` when the stored bytes do not match the
recorded digest. The dashboard checks the digest again before saving the file.

## Configuration

| Setting | Meaning |
|---|---|
| `DEVLOG_DELIVERY_ADAPTER` | `disabled` (default) or `gitea-issue-comment`. |
| `DEVLOG_DELIVERY_GITEA_ISSUE` | The one destination, `owner/repo#number`. |
| `DEVLOG_DELIVERY_ALLOWED_REPOS` | Comma-separated `owner/repo` slugs that may receive deliveries. The destination repository must be listed. |

The adapter also needs `GITEA_BASE_URL` and `GITEA_TOKEN`. If any value is
missing or inconsistent, delivery stays disabled and the review reports why.
Requests cannot choose a destination: approvals must name the configured one,
and a delivery is refused when the configuration changed after approval.
Settings are file-managed and need an API restart.

## Approval policy

`POST /api/devlog/reports/{run_id}/approvals` (admin role):

```json
{"artifact_sha256": "<64 hex>", "destination": "owner/repo#12", "decision": "approved", "reason": "reviewed"}
```

- The run must be a succeeded Devlog run with a `devlog.md` artifact
  (409 `report-not-ready`).
- `artifact_sha256` must equal the current report digest (409
  `stale-report`), and `destination` must equal the configured destination
  (409 `destination-mismatch`).
- The decision records the actor, reason, report artifact ID and digest,
  adapter, and destination. Approvals expire after 24 hours.
- A `rejected` decision blocks delivery. A rejection recorded after an
  approval of the same digest and destination also blocks that approval.

## Delivery

`POST /api/devlog/reports/{run_id}/deliveries` (admin role):

```json
{"approval_id": "devlog-approval-…", "artifact_sha256": "<64 hex>"}
```

The idempotency key is the SHA-256 of the adapter, destination, and report
digest. Each request:

1. Returns `already-delivered` (200) without contacting the destination when a
   delivered receipt exists for the key.
2. Refuses with 409 when the approval is missing (`approval-required`),
   rejected or superseded by a rejection (`approval-rejected`), expired
   (`approval-expired`), bound to another report version (`stale-approval`),
   or bound to another destination (`destination-changed`).
3. Claims the receipt. A concurrent attempt gets 409 `delivery-in-progress`;
   a claim older than two minutes can be taken over.
4. Asks the adapter whether the destination already holds the key's marker.
   If so, the receipt is marked delivered and `reconciled` (200) and nothing
   is posted. If the destination cannot be searched, the attempt fails without
   posting.
5. Posts the report and records `delivered` (201), or records `failed` with a
   redacted error (502). Repeating the same request retries.

The posted body is the exact `devlog.md` bytes, a footer naming the run, the
report digest, and the source window, and the hidden marker
`<!-- switchyard-devlog-delivery:<key> -->`. Bodies above 60,000 bytes are
refused (409 `report-too-large`); export the report instead.

### Gitea issue comment adapter

The adapter lists the destination issue's comments (up to 20 pages of 50) and
treats a comment as the delivery only when it contains the marker and was
written by the account behind `GITEA_TOKEN`. When the issue has more comments
than the scan covers, the adapter reports an error instead of posting, because
it cannot rule out an earlier delivery.

## Receipts

Approvals and receipts are stored in `devlog_delivery_approvals` and
`devlog_delivery_receipts` (migration `035_devlog_delivery.sql`), with one
receipt row per idempotency key. They do not reference `runs` by foreign key,
so delivery evidence survives run retention. The memory store implements the
same contract.
