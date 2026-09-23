# Switchyard Current Status

This page is the current-state index. Dated implementation plans are historical
records; production claims apply only to the exact revision named by a release
qualification record.

## Development target

- Authoritative branch target: `main`
- Current deployed release: `v0.1.2`
- Current deployed revision: `7d90c065c05bfa93eb380ff071036d61c1f965e2`
- The two pointers above name the last release with a revision-matched
  qualification record; `make docs-check` binds them to that record. They are
  not a claim about which revision currently runs. Tags `v0.2.0` through
  `v0.2.4` (2026-08-25 to 2026-08-27) and later `main` revisions have been
  deployed without a qualification record. On 2026-09-23 the Kvant stack was
  observed running `ca44597f2926fc24e77936d365a365908fb8f5a8`. Qualifying a
  current candidate and publishing its record is Gitea issue #14.
- Required local gate: `make verify`
- Required release gate: `make verify-production`
- Required production configuration gate: `make production-config-check` with the deployed environment exported
- Required on-host production contract gate: `make production-smoke` proves the deployed config, systemd and nft state, listener ownership, and revision-bound deployment from the host
- Required remote deployment proof: dispatch `.gitea/workflows/prove-production-deployment.yml` for the exact annotated deployed tag to prove the live web, authenticated control, model, and public authentication paths plus source revision and deployment identity

The remote proof does not inspect or qualify host config, systemd, nft, or local
listener ownership. Keep both production gates.

Record the exact candidate and deployed SHA in `docs/releases/`.

## Capability status

| Capability | State | Evidence |
| --- | --- | --- |
| Durable linear workflow runtime | test-verified | Workers, scheduler, leases, retries, cancellation, pipeline snapshots, memory/PostgreSQL contracts |
| Native model broker and compatible ingress | test-verified | OpenAI/Ollama routes, durable ledger, bounded queue, status and metrics |
| Native OpenCode execution | test-verified | Session correlation, authenticated abort-on-cancel, strict source-checkout-free and shell-denied sidecar with read-only Git-common and managed-worktree access, non-root API/verifier, static Compose isolation contract, pinned runtime, and no default MCP/global permission surface |
| Open Pilot policy pipeline | implemented | Issue-to-PR/CI/merge/close path exists; mutation remains deny-by-default |
| Devlog and Changemaker policy | implemented | Native model-backed linear pipelines |
| Agent profiles | test-verified | Registry/API, executor/model validation, default resolution, prompt contracts, and run-step audit correlation |
| Operator UI | test-verified | Native overview, profiles, queues, and cockpit; intentionally read-only status surfaces replace unsafe placeholder editors |
| Scale and lifecycle | test-verified | Bounded database summaries, 24-hour broker observability, inline artifact integrity/retention metadata, and durable 90-day compatibility usage |
| Release qualification | qualified-with-exceptions | `v0.1.2` is deployed with revision-matched CI, extended soak, production-contract, monitoring, and rollback evidence; see `docs/releases/v0.1.2-qualification.md` |
| Production recovery | test-verified | Versioned policy, completed-bundle, PostgreSQL object, provider-neutral restore evidence, opt-in timer, deadline metrics, and external alerts are test-verified. Historical production restore and rollback evidence predates this contract and does not qualify it. |

`docs/capabilities-v1.json` is the machine-readable capability contract. Its
implementation and test paths bind each status claim to repository evidence.

## Active release blockers

Do not increase unattended Open Pilot autonomy until the following is proven
for one immutable revision:

1. Gitea issue #14: run the documented production qualification for the exact
   current candidate and publish a release record naming the candidate and
   deployed SHAs. Its former blockers are closed: #13 (release evidence
   validator), #16 (alert owner and acknowledgement drill), #18 (encrypted
   Almaz-to-Kvant path) and #19 (encrypted proxy smoke). #18 and #19 were
   closed on 2026-08-24 with production smoke evidence on revision
   `9521e5439870e5661a4fa197e30ae8febc66aa24`; that evidence applies to that
   revision only.

Issue #17 adds static enforcement for the production configuration contract. It does not prove the deployed target path for any later revision.

Production configuration validation also enforces strict OpenCode mode, a
non-default password, a dedicated externally located workspace with matching
non-root ownership, a valid top-level Git repository, and a private managed
worktree marker/layout. Only the API mounts the source workspace; OpenCode and
verifier mount its Git common directory read-only plus the managed worktree
subtree at write/read-only modes respectively. The persistent executor still sees sibling worktrees. The verifier
has Git, pinned Go 1.26, Make, npm, pnpm 10.33.0, and Cargo with tmpfs caches;
its internal network and read-only worktrees limit it to dependency-free or
already-snapshotted checks. Arbitrary dependency installation is not claimed.

## Operational evidence

`docs/development-readiness-report-2026-07-19.md` is historical evidence for
commit `41813c9`. It must not be used as evidence for a later commit. Current
production evidence is recorded in `docs/releases/v0.1.2-qualification.md`.

## Active references

- Architecture: `docs/adr/0003-native-switchyard-modules.md`
- Roadmap: `docs/roadmap.md`
- Release and rollback: `docs/runbooks/release-and-rollback.md`
- Backup and restore: `docs/runbooks/postgres-backup-and-restore.md`
- Incident response: `docs/runbooks/incident-response.md`
- Model ingress: `docs/runbooks/native-model-broker-cutover.md`
- Client compatibility: `docs/reference/client-compatibility.md`
