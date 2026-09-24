# Devlog repository evidence

By default, Devlog reports recent Switchyard run history. Three run inputs add
read-only Gitea evidence for commits, pull requests, releases, and tags. Without
those inputs, the run-history mode, its step summaries, and its deterministic
Markdown fallback are unchanged.

## Inputs

| Input | Required | Format |
|---|---|---|
| `repositories` | yes | Comma-separated `owner/repo` slugs or registered aliases, at most 8 after deduplication. |
| `since` | yes | RFC 3339 timestamp or `YYYY-MM-DD` (UTC midnight). Inclusive. |
| `until` | yes | RFC 3339 timestamp or `YYYY-MM-DD`. Exclusive. At most 93 days after `since`. |

If any of the three inputs is set, all three are required. Example:

```bash
curl -X POST "$SWITCHYARD/api/runs" -H "Authorization: Bearer $TOKEN" \
  -d '{"workflow_id":"devlog","inputs":{"repositories":"subculture-collective/switchyard","since":"2026-09-14","until":"2026-09-21"}}'
```

### Allowed repositories

Run inputs select from a list that the operator configures. They cannot add
repositories to it. The list consists of:

- every slug and alias in the Open Pilot repository registry (the runtime
  manifest, or the legacy singleton repository), resolved to the canonical slug;
- `DEVLOG_ALLOWED_REPOS`, a comma-separated list of additional `owner/repo`
  slugs.

The registry defines the allowed repositories regardless of the Open Pilot
activation mode, because Devlog only reads from them. A repository outside the
list fails `devlog.collect-history` with an error such as:

```
devlog evidence input: repository "owner/other" is not allowed by configuration; allowed repositories: owner/alpha, owner/beta
```

Input errors (including a malformed or oversized window) are deterministic.
They are classified non-retryable, so the run pauses rather than retrying, and
no Gitea request is made.

## Collection

Collection uses the shared Gitea client (`GITEA_BASE_URL`, `GITEA_TOKEN`) and
only issues `GET` requests:

| Source | Endpoint | Notes |
|---|---|---|
| `commits` | `/repos/{owner}/{repo}/commits?since=&until=` | Default branch; filtered again by committer date. |
| `pull_requests` | `/repos/{owner}/{repo}/pulls?state=all&sort=recentupdate` | Paging stops early once a full page predates the window. |
| `releases` | `/repos/{owner}/{repo}/releases` | Draft releases are ignored. Paging stops early once a full page predates the window. |
| `tags` | `/repos/{owner}/{repo}/tags` | Provides the exact commit behind each release tag. |

Each source reads at most 4 pages of 50 entries per repository. A source that
reaches the cap is marked `truncated`. The report keeps at most 400 items,
newest first.

Missing configuration is reported, and no activity is invented for it:

- `GITEA_BASE_URL` unset, or `GITEA_TOKEN` unset: no request is made and every
  source is `missing`, with the reason.
- One source fails (for example, a 403 on tags): that source is `failed` with a
  redacted error; the other sources are still reported.

In both cases the run still succeeds. `complete` is `false`, a
`devlog.evidence.missing` event names each gap, and the Markdown lists the gaps
under "Missing or incomplete sources".

## Classification

| Status | Meaning |
|---|---|
| `merged` | Pull request merged inside the window, or a commit on the default branch inside the window, with no deployment evidence. |
| `deployed` | A merged item whose exact commit is the target of a published release or a tag. `deployment` names the release or tag. |
| `proposed` | Pull request opened inside the window, or still open and updated inside it. |
| `closed` | Pull request closed without merging inside the window. |
| `released` | Published release inside the window. |
| `tagged` | Tag whose commit falls inside the window and that belongs to no release. |

A change is marked deployed only on an exact commit match. Devlog does not infer
ancestry, so a change that shipped inside a later release, but is not that
release's tagged commit, stays `merged`.

Deduplication:

- A commit that is a merged pull request's merge commit or head commit folds
  into the pull request item, which then also carries the commit link.
- A release tag folds into its release item.
- Repeated entries across pages, and aliases of the same repository, produce a
  single item.
- Item IDs are stable (`owner/repo#pr/N`, `owner/repo@<sha>`,
  `owner/repo@release/<tag>`, `owner/repo@tag/<tag>`), so an item in the overlap
  of two windows keeps the same ID. Windows are half-open, so an item at
  exactly `until` belongs to the next window.

## Output

In evidence mode, `devlog.collect-history` attaches `devlog-evidence.json`
(kind `json`, version 1) and publishes step outputs:

| Output | Value |
|---|---|
| `evidence_json` | The report JSON. Omitted, with a `step.output.omitted` event, when it exceeds `SWITCHYARD_MAX_STEP_OUTPUT_BYTES`; the artifact is always attached. |
| `evidence_sha256` | SHA-256 of the report JSON. |
| `evidence_items`, `evidence_missing` | Item count and missing-source count. |
| `evidence_complete` | `true` when no source was missing, failed, or truncated. |
| `evidence_repositories` | Canonical slugs, comma-separated. |
| `evidence_since`, `evidence_until` | The window in RFC 3339 UTC. |
| `artifact_name` | `devlog-evidence.json`. |

Report shape:

```json
{
  "version": 1,
  "provider": "gitea",
  "window": {"since": "2026-09-14T00:00:00Z", "until": "2026-09-21T00:00:00Z"},
  "complete": true,
  "limits": {"max_repositories": 8, "page_size": 50, "max_pages_per_source": 4, "max_items": 400},
  "repositories": [
    {"repository": "owner/alpha", "sources": [
      {"source": "commits", "status": "collected", "items": 3, "pages": 1}
    ]}
  ],
  "items": [
    {
      "id": "owner/alpha#pr/10",
      "repository": "owner/alpha",
      "kind": "pull_request",
      "status": "deployed",
      "title": "Add feature",
      "number": 10,
      "ref": "main",
      "sha": "c1…",
      "author": "dev",
      "occurred_at": "2026-09-15T09:00:00Z",
      "source_url": "https://git.example/owner/alpha/pulls/10",
      "links": [{"kind": "pull_request", "url": "…/pulls/10"}, {"kind": "commit", "url": "…/commit/c1…"}],
      "sources": ["pull_requests", "commits"],
      "deployment": {"kind": "release", "ref": "v1.0.0", "sha": "c1…", "url": "…/releases/tag/v1.0.0"},
      "window": {"since": "2026-09-14T00:00:00Z", "until": "2026-09-21T00:00:00Z"}
    }
  ],
  "missing": []
}
```

Every item has a non-empty `source_url` and the collection `window`. An item
with no link is dropped rather than reported without provenance.

`devlog.md` gains a "Repository evidence" section, grouped by repository and
status. Every item line contains its source link and the window. When the model
rewrites the Markdown, it receives an evidence-specific system prompt, and the
deterministic section is appended after its text as "Source record:", so the
links survive the rewrite. If the model is unavailable, the deterministic
Markdown is used unchanged.

## Retries and resume

If `devlog.collect-history` already succeeded and a later step is retried or
resumed, the later step collects the evidence again for the same window before
it renders Markdown. Activity inside a past window rarely changes, but a later
collection can differ if, for example, a release was published in between. The
attached `devlog-evidence.json` from the original step remains the recorded
evidence.

No database migration is involved.
