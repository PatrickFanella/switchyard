# Switchyard Current Status

This page is the current-state index. Dated implementation plans are historical
records; production claims apply only to the exact revision named by a release
qualification record.

## Development target

- Authoritative branch target: `main`
- Current deployed release: `v0.1.1`
- Current deployed revision: `66fb60a8789ca9c58bc56d70b40fceeeea145a03`
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
| Release qualification | qualified with exceptions | `v0.1.1` is deployed with revision-matched CI and production-contract evidence; see `docs/releases/v0.1.1-qualification.md` |
| Production recovery | partial | A scheduled production backup and isolated restore drill are proven; a safe live application rollback still requires a qualified successor release |

## Active release blockers

Do not increase unattended Open Pilot autonomy until all of the following are
proven for one immutable revision:

1. rehearsed application rollback from a qualified successor to `v0.1.1`;
2. working alert delivery, acknowledgement, recovery notification, and ownership;
3. encrypted authenticated Almaz-to-Kvant proxy transport.

## Operational evidence

`docs/development-readiness-report-2026-07-19.md` is historical evidence for
commit `41813c9`. It must not be used as evidence for a later commit. Current
production evidence is recorded in `docs/releases/v0.1.1-qualification.md`.

## Active references

- Architecture: `docs/adr/0003-native-switchyard-modules.md`
- Roadmap: `docs/roadmap.md`
- Release and rollback: `docs/runbooks/release-and-rollback.md`
- Backup and restore: `docs/runbooks/postgres-backup-and-restore.md`
- Incident response: `docs/runbooks/incident-response.md`
- Model ingress: `docs/runbooks/native-model-broker-cutover.md`
- Client compatibility: `docs/reference/client-compatibility.md`
