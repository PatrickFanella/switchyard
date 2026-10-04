# Automatic Open Pilot repository onboarding

Historical planning record for the October 4 request. Implementation and production activation remain in progress; this document is not qualification evidence.

The requested behavior is discovery of every repository visible to the existing
Gitea runtime account, including repositories created later, with automatic
preparation when eligible work appears. Existing executor identities, worktrees,
proof history, credentials, and verification boundaries must survive onboarding.

## Implementation sequence

1. Add `OPEN_PILOT_POLL_REPOS=*` for every currently prepared registry entry.
   Preserve explicit lists and fail closed when admission is unavailable.
2. Add an operator-owned discovery/reconciliation command. Discover paginated
   repository metadata and eligible open issues using the runtime account. Keep
   a durable catalog independent of the bounded active executor registry.
3. Prepare dedicated clones, private credentials, configuration, and managed
   worktree roots for repositories with eligible work. Preserve default branches
   and repository CI contexts instead of inheriting Switchyard-specific values.
4. Reconcile the bounded executor pool only when the runtime is idle. Preserve
   pinned repositories and any repository with unfinished durable state. Reuse
   stable identities and retained workspace paths after an idle slot is retired.
5. Render and validate the full candidate topology before activation. Preserve
   prior topology and poll configuration; use the authenticated rollout API for
   fingerprint transitions, never database writes or relaxed admission.
6. Verify discovery pagination, later additions, eligibility, identity stability,
   private preparation, pool capacity, busy-runtime deferral, and recovery with
   isolated fixtures. Run `make verify`, then enable the operator timer and prove
   live catalog, scheduling, polling, readiness, and a bounded preparation.

## Limits and recovery

Discovery does not approve arbitrary test commands or waive required checks.
Archived/read-only repositories remain visible with an explicit blocker.
Automatic preparation runs outside executors and verifiers. Executors retain
strict configuration and isolated repository mounts; verifiers remain offline.
The catalog is not limited by the active registry count. Capacity shortages keep
work pending for a later reconciliation. No workspace or history is deleted.
Production activation must preserve the existing environment and deployed images.
