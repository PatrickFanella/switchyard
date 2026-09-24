# Development Readiness Report — 2026-07-19

> **Historical evidence for commit `41813c9` only.** The verdict below was true on 2026-07-19 for that commit. It is not evidence for any later revision; see `docs/status.md` and `docs/releases/`.

## Verdict

**Production-qualified and deployed.** Switchyard passes its complete local and actual-runner production qualification. Commit `41813c9` is deployed on Kvant, the public dashboard is protected by Almaz Caddy and Authelia, and listener-isolation and model-generation smoke checks pass.

## What changed

- Production verification is codified in Gitea: baseline push/PR verification and scheduled/manual full qualification have pinned actions, explicit tool contracts, timeouts, concurrency control, clean-checkout Compose credentials, and a real Buildx SBOM/provenance preflight.
- The dedicated `switchyard-production` runner image pins its base and Go toolchain by digest. Docker-socket tests use the runner network without publishing test databases or APIs.
- pnpm configuration uses `web/pnpm-workspace.yaml`; frozen installs no longer emit the ignored `package.json#pnpm.overrides` warning.
- The control plane was decomposed by existing responsibilities without changing package boundaries: HTTP registration/handlers, memory/PostgreSQL persistence, and workflow executors now live in focused files. OpenAPI reverse coverage uses the Go AST and explicit action routes.
- Lifecycle contracts now cover memory and PostgreSQL queueing, exclusive claims, cancellation, retry/backoff, lease recovery/exhaustion, correlation merging, atomic backend-attempt evidence, panic terminalization, and recovered running steps.
- Frontend coverage now has strict typed fetch fixtures and 19 tests for trigger dispatch, retry/cancel, Open Pilot resume, error/selection behavior, and hash navigation.
- Supported OpenAI/Ollama-compatible ingress remains supported. Deprecated llama-line and OCQ backend selectors now produce route-specific, namespaced evidence only at actual outbound attempts. JSON, SSE, asynchronous, workflow, error, retry, recovery, and no-attempt paths are covered; the unreachable external Changemaker executor was removed while its read-only migration probe remains.
- Web ingress binds independently from API/Postgres, source-allows only the exact Almaz proxy address, and strips spoofable `X-Forwarded-User` before forwarding Authelia's `Remote-User` identity.

## Qualification evidence

The following passed locally and on the actual Gitea runner on 2026-07-19:

| Gate | Evidence |
| --- | --- |
| `make verify` | Go formatting/vet/tests, 19 Vitest cases, TypeScript, ESLint, web and Go builds, reproducible Go binaries, Compose config, Open Pilot template checks |
| `go test -race ./...` | 329 control-plane cases plus app/CLI/telemetry packages |
| `make api-soak` | 600 authenticated requests across 8 clients over 30 seconds; graceful shutdown passed |
| `make postgres-test` | Migrations, PostgreSQL integration, and lifecycle contract including atomic backend decisions and terminal panic/lease step closure |
| `make security-audit` | `govulncheck`, pnpm high-severity audit, and OpenCode npm high-severity audit |
| `make docker-smoke` | Pinned image builds, authentication, readiness, listener isolation, migrations, and shutdown |
| `make release-verify` | API, web, and OpenCode OCI archives with pinned SBOM/provenance; all SHA-256 checksums verified |
| `make verify-production` | Complete aggregate gate passed |
| Gitea run `1218` | Baseline verification passed on `almaz-docker-01` for commit `41813c9` |
| Gitea run `1219` | Complete production qualification passed on `almaz-docker-01` for commit `41813c9` |
| Kvant deployment smoke | Compose services healthy; control API loopback-only; model ingress LAN-bound; JSON/OpenAI model generation returned `ok`; public route redirects to Authelia |
| Web trust-boundary smoke | Almaz health `200`, spoofed `X-Forwarded-User` `401`, explicit proxy identity `200`; direct Kvant/NUC access `403` |

The compatibility phase received a final Oracle **GO** with no blockers or important concerns after 329 race-tested cases and the PostgreSQL contract passed.

## Remaining operational work

These are operational prerequisites, not unresolved implementation failures:

1. Exercise a full PostgreSQL restore from the verified pre-deployment custom-format backup and retain it according to an operator backup policy.
2. Add production alerting, capacity thresholds, and a rehearsed application/database rollback procedure.
3. Replace the Almaz-to-Kvant trusted-LAN HTTP fallback with a Tailscale ACL, mTLS, or another authenticated encrypted proxy hop.
4. Keep deprecated llama-line/OCQ selectors until the documented removal policy is satisfied: migration documentation, at least 90 days, and two consecutive releases with zero observed deprecated-backend correlations/events.
5. For higher traffic targets, run a longer environment-specific soak than the default 30-second gate.

## Risk assessment

- **Correctness/durability:** low for release-candidate promotion; memory/PostgreSQL parity, race, retry, cancellation, lease, and panic paths are covered.
- **Supply chain/reproducibility:** low; immutable build inputs, vulnerability audits, reproducible binaries, and attested OCI artifacts passed.
- **Maintainability:** improved; concentrated control-plane files were split while retaining one cohesive package and native Switchyard boundaries.
- **Frontend regression risk:** moderate-low; critical operator mutations are covered, though the UI suite remains smaller than backend coverage.
- **Deployment risk:** moderate-low after actual-runner qualification and live smoke tests; residual risk is concentrated in backup restore rehearsal, monitoring/rollback operations, and the plaintext trusted-LAN proxy hop.

## Conclusion

No code-level blocker remains from this readiness program. Switchyard is deployed and healthy; continued operation should focus on monitoring, backup restoration, rollback rehearsal, and replacing the trusted-LAN proxy hop.
