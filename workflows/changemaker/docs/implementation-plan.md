# Changemaker Implementation Plan

Changemaker is a daily dev-update generator for public Gitea activity. It crawls repositories, reads pull requests and commits, summarizes code changes, maintains narrative continuity, and writes a conversational Markdown blog post plus separate Twitter/X drafts for supporters who want understandable product progress, not raw commit logs.

## Product defaults

- **Source:** `https://git.subcult.tv` by default, configurable for any Gitea instance.
- **Scope:** all public repositories by default; owner, repo, branch, and date filters available.
- **Daily window:** by default, end the next run at the previous recorded post time plus 24 hours, then fetch the preceding 36 hours to avoid missed late-night work.
- **Post scope:** write about uncovered changes since the last generated/approved post, framed as "since the last update".
- **Coverage filter:** exclude PRs and commits already linked in prior generated posts. The CLI only records coverage when `--mark-covered` is used; server runtime defaults may mark covered through `data/admin-config.json`.
- **First-run fallback:** if no previous post exists, end the window at the current run time.
- **Pull requests:** include open, closed, and merged PRs.
- **Commits:** include all branches so work not merged to main can still appear.
- **Open work framing:** automatically describe open PRs and unmerged branch work as in progress, experimental, or still in motion.
- **Links:** use inline wiki-style Markdown links such as `[[owner/repo#42]](https://...)` and `[[owner/repo@abc123]](https://...)`.
- **Narrative memory:** auto-update after every successful post and keep the last 7 completed post summaries available for the next run.
- **Report date:** title/header dates default to one day before publish time, because the daily post usually describes the previous day.
- **Period posts:** daily generation may also create weekly, monthly, and quarterly posts on due days; these are additional posts and do not replace the daily post.

## Gitea collection

Use the Gitea API, not git clone, for V1.

Primary endpoints:

```text
GET /api/v1/repos/search
GET /api/v1/repos/{owner}/{repo}/pulls?state=all
GET /api/v1/repos/{owner}/{repo}/pulls/{index}
GET /api/v1/repos/{owner}/{repo}/pulls/{index}.diff
GET /api/v1/repos/{owner}/{repo}/pulls/{index}/commits
GET /api/v1/repos/{owner}/{repo}/commits
GET /api/v1/repos/{owner}/{repo}/git/commits/{sha}.diff
```

Pagination must use `page`, `limit`, `Link`, and `x-total-count` where available.

## Change model

Normalize raw Gitea records into events, then cluster related events before writing.

```ts
type ChangeEvent = {
  id: string;
  type: 'pull_request' | 'commit';
  repo: string;
  branch?: string;
  title: string;
  body?: string;
  diff?: string;
  author?: string;
  url: string;
  createdAt: string;
  updatedAt: string;
  mergedAt?: string;
  state?: 'open' | 'closed' | 'merged';
  coveredByPostId?: string;
};

type ChangeCluster = {
  theme: string;
  events: ChangeEvent[];
  summary: string;
  narrativeImportance: 'major' | 'minor' | 'background';
};
```

Deduplication rule: prefer PR summaries over their contained commits, but keep commit evidence so standalone branch work is not lost.

## Diff handling

Never send large raw diffs directly to the writer. First filter and summarize.

Default filters:

- exclude binary diffs
- exclude generated/vendor/dist files
- exclude lockfiles from prose unless they are the only meaningful change
- cap changed files per event
- cap hunks per file
- preserve filenames, stats, and representative hunks

Pipeline:

```text
fetch diff -> filter diff -> summarize files -> summarize change event -> cluster themes -> write post
```

## Narrative memory

Persist narrative state so daily posts connect across time.

```json
{
  "projectContext": {
    "name": "changemaker",
    "audience": "technical supporters following product progress",
    "tone": "technical, candid, product-progress, Patreon-friendly"
  },
  "activeThreads": [],
  "futureWork": [],
  "recentPosts": []
}
```

Writer rules:

- transform commit and PR facts into an introduction/body/conclusion narrative
- connect today's changes to active threads and the last 7 recent post summaries
- explain technical work in accessible language without hiding source links
- mention likely next steps only when supported by open PRs, branches, issues, or narrative state
- do not claim open or unmerged work has shipped
- include 3-4 concise Twitter/X drafts with suggested posting times in a separate social section for non-empty updates
- include inline wiki-style source links

## LLM providers

Start with OpenAI. Keep the provider boundary narrow so llama-line and an OpenCode adapter can follow.

```ts
interface LlmProvider {
  summarizeChange(input: unknown): Promise<unknown>;
  clusterChanges(input: unknown): Promise<unknown>;
  writePost(input: unknown): Promise<unknown>;
  updateNarrativeState(input: unknown): Promise<unknown>;
}
```

Initial providers:

1. `openai`
2. `llama-line`
3. `opencode` adapter, later

## Storage

Use SQLite from the start to avoid repeated summarization and to track covered changes.

Tables:

- `repos`
- `change_events`
- `change_summaries`
- `change_clusters`
- `generated_posts`
- `post_change_links`
- `narrative_threads`
- `provider_cache`
- `runs`

Cache keys should include repo, PR index or commit SHA, diff hash, model, and prompt version.

## CLI shape

```bash
changemaker generate [--base-url <url>] [--token <token>] [--token-env <name>] [--owner <owner>] [--repo <repo>] [--out-dir <dir>] [--db-path <path>] [--include-covered] [--mark-covered] [--no-diffs] [--max-diff-chars <chars>] [--summarize] [--post] [--narrative-state-path <path>] [--no-narrative-update] [--llm-provider <provider>] [--llm-model <model>] [--llama-line-base-url <url>] [--llama-line-api-key <key>] [--summary-retries <count>] [--since <date>] [--until <date>] [--lookback-hours <hours>] [--cadence-hours <hours>] [--now-window]
changemaker changes inspect --file <path>
changemaker narrative inspect [--file <path>]
changemaker backfill --start <date> --end <date> --cadence daily|weekly [--time-travel-strict]
```

Future commands:

```bash
changemaker narrative edit
```

Backfill should walk history chronologically and avoid leaking future knowledge into old posts unless an explicit retrospective mode is added.

## Output

V1 writes local artifacts:

```text
out/YYYY-MM-DD-post.md
out/YYYY-MM-DD-changes.json
out/YYYY-MM-DD-narrative-state.json
```

Markdown structure:

```md
# <date> field report: <theme>

## Introduction

<opening narrative>

## What changed

<clustered summary of public changes>

## Why it matters

<accessible explanation of impact>

## What to watch next

<supported next-step framing>

## Conclusion

<closing paragraph>

## Tweet drafts

1. <draft>
2. <draft>
3. <draft>
4. <draft>

## Receipts

- [[owner/repo#42]](https://...)
- [[owner/repo@abc123]](https://...)
```

Future renderers should consume the same post/change JSON instead of re-crawling Gitea:

- Patreon-formatted post
- tweet/thread
- short script/topic list
- image prompts
- charts and change visualizations
- newsletter variant

## Implementation phases

1. Convert template metadata and docs to Changemaker.
2. Scaffold CLI, config loading, and command help.
3. Build Gitea API client with auth and pagination.
4. Crawl public repositories with owner/repo filters.
5. Collect PRs and commits within the 36-hour window.
6. Normalize, dedupe, and mark already-covered changes.
7. Fetch and filter PR/commit diffs.
8. Add SQLite cache and run metadata.
9. Implement OpenAI summarization provider.
10. Cluster changes into narrative themes.
11. Generate Markdown and JSON artifacts.
12. Auto-update narrative state.
13. Add inspect/debug commands.
14. Add llama-line provider.
15. Add backfill mode.
16. Add social/media/chart renderers.

## Verification strategy

- Unit-test URL construction, pagination, normalization, dedupe, and diff filtering.
- Mock Gitea responses for crawler tests.
- Snapshot generated Markdown from fixed summaries.
- Keep `make verify` as the canonical check.
- Admin server ships as `changemaker-server`, requires `CHANGEMAKER_ADMIN_TOKEN`, and reuses the normal generation flow for run requests when env/config is available.
