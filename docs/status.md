# Switchyard Current Status

This page is the current-state index. Dated implementation plans are historical
records; production claims apply only to the exact revision named by a release
qualification record.

## Development target

- Authoritative branch target: `main`
- Current integration source: `codex/production-remediation`
- Required local gate: `make verify`
- Required release gate: `make verify-production`
- Required deployment proof: `make production-smoke` on the resolved target

Record the exact candidate and deployed SHA in `docs/releases/`.

## Capability status

| Capability | State | Evidence |
| --- | --- | --- |
| Durable linear workflow runtime | test-verified | Workers, scheduler, leases, retries, cancellation, pipeline snapshots, memory/PostgreSQL contracts |
| Native model broker and compatible ingress | test-verified | OpenAI/Ollama routes, durable ledger, bounded queue, status and metrics |
| Native OpenCode execution | test-verified | Session correlation, authenticated abort-on-cancel, non-root strict sidecar, pinned runtime, and no default MCP/global permission surface |
| Open Pilot policy pipeline | implemented, gated | Issue-to-PR/CI/merge/close path exists; mutation remains deny-by-default |
| Devlog and Changemaker policy | implemented | Native model-backed linear pipelines |
| Agent profiles | test-verified | Registry/API, executor/model validation, default resolution, prompt contracts, and run-step audit correlation |
| Operator UI | test-verified selected scope | Native overview, profiles, queues, and cockpit; intentionally read-only status surfaces replace unsafe placeholder editors |
| Scale and lifecycle | test-verified | Bounded database summaries, 24-hour broker observability, inline artifact integrity/retention metadata, and durable 90-day compatibility usage |
| Release qualification | implemented | Baseline and production Make gates, runner CI, reproducible artifacts and source manifest |
| Production recovery | partial | Backup/restore/rollback assets exist; real target drills and alert delivery must be recorded |

## Active release blockers

Do not increase unattended Open Pilot autonomy until all of the following are
proven for one immutable revision:

1. full `make verify-production` for the immutable release commit;
2. verified production backup and isolated restore;
3. rehearsed application rollback;
4. working alert delivery and ownership;
5. encrypted authenticated Almaz-to-Kvant proxy transport;
6. production contract smoke from host, public route, and affected remote client.

## Operational evidence

`docs/development-readiness-report-2026-07-19.md` is historical evidence for
commit `41813c9`. It must not be used as evidence for a later commit. New
promotion evidence belongs in `docs/releases/<version>-qualification.md`.

## Active references

- Architecture: `docs/adr/0003-native-switchyard-modules.md`
- Roadmap: `docs/roadmap.md`
- Release and rollback: `docs/runbooks/release-and-rollback.md`
- Backup and restore: `docs/runbooks/postgres-backup-and-restore.md`
- Incident response: `docs/runbooks/incident-response.md`
- Model ingress: `docs/runbooks/native-model-broker-cutover.md`
- Client compatibility: `docs/reference/client-compatibility.md`
