# Switchyard Current Status

This page is the current-state index. Dated implementation plans are historical
records; production claims apply only to the exact revision named by a release
qualification record.

## How to read status claims

Reconciled against `main` on 2026-09-23 (Gitea issue #65). Four levels are
distinguished throughout this page and `docs/roadmap.md`:

- **implemented**: the code path exists on `main`.
- **test-verified**: `make verify` exercises it with the tests named in
  `docs/capabilities-v1.json`.
- **deployed**: a revision runs on the production host. Deployment alone is
  not qualification.
- **qualified**: a revision-matched record exists in `docs/releases/`.

Two reading rules follow from the inconsistencies this reconciliation found:

1. Unchecked boxes in historical plans (`docs/superpowers/plans/`,
   `docs/production-remediation-plan.md`) are original planning notation.
   They do not mean the work is missing; the capability table below is the
   current claim.
2. A closed tracker issue proves only what its closing evidence names, for the
   revision it names. It is not proof for a newer release. See "Tracked
   issues" for the observed state of every issue this page and the roadmap
   cite.

## Development target

- Authoritative branch target: `main`
- Current deployed release: `v0.1.2`
- Current deployed revision: `7d90c065c05bfa93eb380ff071036d61c1f965e2`
- Release tags without a qualification record: `v0.2.0`, `v0.2.1`, `v0.2.2`, `v0.2.3`, `v0.2.4`
- The two pointers above name the last release with a revision-matched
  qualification record; `make docs-check` binds them to that record and fails
  if a newer tag appears without either a record or an entry on the line
  above. They are not a claim about which revision currently runs. The listed
  tags (2026-08-25 to 2026-08-27) and later `main` revisions have been
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
| Evidence search | test-verified | Authenticated, retention-bounded `/api/evidence/search` over runs, steps, and model requests with stable cursor pagination, payload exclusion, and redaction on memory and PostgreSQL stores; see `docs/reference/evidence-search.md` |
| Scale and lifecycle | test-verified | Bounded database summaries, 24-hour broker observability, inline artifact integrity/retention metadata, and durable 90-day compatibility usage |
| Release qualification | qualified-with-exceptions | `v0.1.2` is the last release with revision-matched CI, extended soak, production-contract, monitoring, and rollback evidence; see `docs/releases/v0.1.2-qualification.md`. Later deployed tags and `main` revisions are unqualified (see Development target and #14) |
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
   revision only. #14 is unblocked and open; it is human-run on the
   production host and is not queued for an agent.

Issue #17 (closed) adds static enforcement for the production configuration contract. It does not prove the deployed target path for any later revision.

## Completed foundations

These tracker issues are closed. Each one landed code or evidence on `main`
that later work relies on. None of them qualifies a release: the only current
qualification record remains `v0.1.2`, and the current candidate is tracked by
#14.

| Issue | Foundation | What it does not prove |
| --- | --- | --- |
| #13 | Revision-bound release evidence validator (`scripts/validate_release_evidence.py`, schema in `docs/releases/`) | A record for any release after `v0.1.2` |
| #16 | Alert owner assigned; Alertmanager-to-ntfy firing, resolved delivery, and human acknowledgement drill recorded 2026-08-24 | Alert delivery for a later revision or a changed route |
| #17 | Static production configuration contract checks (`scripts/validate_production_config.py`) | The live target path of any deployed revision |
| #18 | Approved encrypted Almaz-to-Kvant Tailscale path provisioned | Transport on any revision other than `9521e5439870e5661a4fa197e30ae8febc66aa24` |
| #19 | Encrypted proxy path production smoke on `9521e5439870e5661a4fa197e30ae8febc66aa24` | The same for later revisions |
| #24 | Scheduled disposable restore-drill evidence capture (`scripts/restore_drill_scheduler.py`, opt-in timer) | A live production timer execution; none is claimed here |
| #25 | External monitoring and CI-runner responsibility assigned to the repository owner (2026-08-24) | Ongoing access review; the issue records the assignment only |
| #28 | Inline-artifact growth measurement and storage decision (`docs/adr/0004-inline-artifact-storage-threshold.md`) | A threshold breach or object-storage selection; none has occurred |
| #29 | Deprecated-selector retirement eligibility gate | Completion of the 90-day observation or two zero-use releases |
| #30 | Implementation-to-documentation drift checks in `scripts/check_docs_consistency.py` | Tracker state; the checks read only the repository |

Roadmap #60 is the open parent for operator-experience and workflow work.
#61 (closed) reconciled the production Open Pilot branch with `main`. #65
(this reconciliation) is closed by the pull request that lands this page.

## Open Pilot fixtures and proof stubs

The files below exist under `docs/` because Open Pilot issues required a
document to pass a grep-style test command. They are fixtures or stubs, not
qualification evidence, and no capability or release claim on this page
depends on them:

- `docs/open-pilot-smoke-test.md` and `docs/open-pilot-bot-identity-smoke.md`:
  harmless fixtures for issue-to-merge automation tests.
- `docs/open-pilot-production-proof.md`: created by Open Pilot to exercise the
  production merge-and-close lifecycle; it links no run, PR, or smoke output.
- `docs/open-pilot-multi-repo-production-proof.md`: the stub required by
  #44–#49. Those six issues shared one body and one test command and were
  closed on 2026-09-23 as duplicates of the stub requirement. The closure
  records that the file exists and passes the grep test; it does not claim a
  current multi-repository production proof. Failed or partial attempts stay
  on their issues.
- `docs/open-pilot-live-two-repository-proof.md`: a one-line claim dated
  2026-08-31 with no linked run, PR, or smoke evidence in this repository.
  Treat it as unverified.

`make docs-check` fails if a new `*proof*`, `*smoke*`, or `*fixture*` document
appears under `docs/` without an entry in this list.

## Tracked issues

Observed with `tea issues <n>` against
`subculture-collective/switchyard` on the date shown. `make docs-check`
requires every issue cited in this page, `docs/roadmap.md`, and
`docs/ui-feature-migration.md` to appear here, and rejects open-work items
that cite a closed issue without saying so. The table is repository evidence
of what was observed; it is not live tracker state.

| Issue | State | Observed | Role |
| --- | --- | --- | --- |
| #7 | open | 2026-09-23 | Epic: revision-matched release qualification (parent of #14) |
| #13 | closed | 2026-09-23 | Release evidence validator |
| #14 | open | 2026-09-23 | Qualify current main and publish immutable release evidence |
| #16 | closed | 2026-09-23 | Alert owner and acknowledgement drill |
| #17 | closed | 2026-09-23 | Secure proxy transport configuration and contract checks |
| #18 | closed | 2026-09-23 | Encrypted Almaz-to-Kvant network path |
| #19 | closed | 2026-09-23 | Encrypted proxy path production smoke |
| #24 | closed | 2026-09-23 | Scheduled restore-drill evidence capture |
| #25 | closed | 2026-09-23 | External monitoring and CI-runner responsibility |
| #28 | closed | 2026-09-23 | Inline-artifact growth and storage decision |
| #29 | closed | 2026-09-23 | Deprecated-selector retirement eligibility gate |
| #30 | closed | 2026-09-23 | Implementation-to-documentation drift checks |
| #44 | closed | 2026-09-23 | Multi-repo proof stub (duplicate set) |
| #45 | closed | 2026-09-23 | Multi-repo proof stub (duplicate set) |
| #46 | closed | 2026-09-23 | Multi-repo proof stub (duplicate set) |
| #47 | closed | 2026-09-23 | Multi-repo proof stub (duplicate set) |
| #48 | closed | 2026-09-23 | Multi-repo proof stub (duplicate set) |
| #49 | closed | 2026-09-23 | Multi-repo proof stub (duplicate set) |
| #60 | open | 2026-09-23 | Roadmap: operator experience and workflow capability completion |
| #61 | closed | 2026-09-23 | Reconcile production Open Pilot work, main, and pending assignment changes |
| #65 | open | 2026-09-23 | This reconciliation of roadmap, status, release pointers, and proof issues |

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
- Evidence search: `docs/reference/evidence-search.md`
- Runtime configuration boundary: `docs/reference/runtime-configuration.md`
