# Changemaker

Daily dev-update generator for public Gitea activity.

Changemaker crawls Gitea repositories, reads pull requests and commits, summarizes code changes, maintains narrative continuity, and writes narrative Markdown updates plus separate Twitter/X drafts for supporters who want understandable product progress.

Created from `subculture-collective/project-template-typescript`.

## Quick start

```bash
make verify
```

## CLI

```bash
changemaker generate [--base-url <url>] [--token <token>] [--token-env <name>] [--owner <owner>] [--repo <repo>] [--out-dir <dir>] [--db-path <path>] [--include-covered] [--mark-covered] [--no-diffs] [--max-diff-chars <chars>] [--summarize] [--post] [--narrative-state-path <path>] [--no-narrative-update] [--llm-provider <provider>] [--llm-model <model>] [--llama-line-base-url <url>] [--llama-line-api-key <key>] [--summary-retries <count>] [--extra-periods weekly,monthly,quarterly] [--report-date-offset-days <days>] [--no-social-section] [--since <date>] [--until <date>] [--lookback-hours <hours>] [--cadence-hours <hours>] [--now-window]
changemaker changes inspect --file out/YYYY-MM-DD-changes.json
changemaker narrative inspect [--file .cache/narrative-state.json]
changemaker backfill --start 2026-01-01 --end 2026-02-01 --cadence daily|weekly|monthly|quarterly [--time-travel-strict]
```

Default `generate` window is cadence-based: if a previous post/change run is recorded in SQLite, the next window ends at `last post + 24h` and looks back 36 hours. Use `--now-window` to force `now - 36h`. The CLI only records covered changes when `--mark-covered` is used; server runs can still default `markCovered` on through runtime config.

When `--post` is enabled, the Markdown post is written as a narrative update rather than a raw changelog. The title format is `# <report date> field report: <theme>`; by default the report date is one day before the publish date. It includes short, scan-friendly sections for the introduction, what changed, why it matters, what to watch next, conclusion, a separate Twitter/X section with suggested posting times, and source receipts. Narrative state keeps the last 7 post summaries so each daily run can connect to the recent story without rereading old Markdown.

Use `--extra-periods weekly,monthly,quarterly` to write additional period posts on due days without replacing the daily post. Weekly posts run on Mondays, monthly posts run on the first day of a month, and quarterly posts run on the first day of a quarter.

## Frontend

```bash
pnpm run dev:web
pnpm run build:web
pnpm run preview:web
```

The frontend lives in `web/` and uses Vite, React, TypeScript, and Tailwind CSS.

## Admin server

```bash
CHANGEMAKER_ADMIN_TOKEN='set-a-long-random-token' pnpm run build && pnpm run server
```

Required env:
- `CHANGEMAKER_ADMIN_TOKEN`
- `CHANGEMAKER_SERVER_HOST` (default `127.0.0.1`)
- `CHANGEMAKER_SERVER_PORT` (default `3038`)
- `CHANGEMAKER_POSTS_DIR` (default `out`)
- `CHANGEMAKER_DATA_DIR` (default `.cache`)
- `CHANGEMAKER_SERVICE_CONTROL=true` to enable systemd actions

Run requests reuse the existing generation pipeline and write only to the configured posts/data dirs.

## Plan

See `docs/implementation-plan.md` for the approved implementation plan.

## Open Pilot

This repository keeps the base Open Pilot issue template and PR template.
Bootstrap labels after creating a repo from this template:

```bash
open-pilot labels bootstrap subculture-collective/changemaker
```

## License

GPL-3.0-only. See `LICENSE`.
