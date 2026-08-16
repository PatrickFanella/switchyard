# Switchyard Production Evidence Contract

## Target identity

Record current values rather than copying historical ones:

- production host and checkout;
- deployed Git revision and worktree state;
- `switchyard.service` unit source and working directory;
- active Docker Compose project and containers;
- effective bind addresses without secret values;
- affected caller, host, or container.

## Deployment path

The repository-owned unit uses:

- start: `docker compose up -d --remove-orphans`;
- reload/redeploy: `docker compose up -d --build --remove-orphans`;
- stop: `docker compose stop`.

Inspect the installed unit before relying on this template. Run the
repository-required verification gate before deployment unless the user
explicitly scopes deployment to an already-qualified immutable revision.

## Evidence matrix

| Layer | Required evidence |
| --- | --- |
| Revision | Intended commit is the deployed checkout/image source |
| Service | `switchyard.service` and Compose report expected active state |
| Process | Application process is alive, not merely its container |
| Control API | Loopback control health and authenticated operator request work |
| Safety | `GET /api/operator-safety` succeeds and warnings are evaluated |
| Model ingress | Canonical `/health`, broker status, and requested generation contract pass |
| Remote caller | The same canonical route works from the affected host/container |
| Isolation | Model ingress does not expose control-plane or administrative routes |
| Upstream | Ollama is reached on its real upstream port, never through Switchyard's own ingress |
| Public web | Public route returns the expected Authelia authentication redirect |
| Proxy boundary | Direct listener access remains denied except for the documented proxy |
| Rollback | Current listener owner and post-rollback health are recorded when used |

Use the native model broker runbook for exact current commands and expected
payloads. Do not normalize or invent alternate health paths.

## Diagnostic order

1. Target and configuration source.
2. Service/container state.
3. Application process and logs.
4. Listener and network reachability.
5. Authentication and proxy assumptions.
6. Application-level health and safety contracts.
7. Upstream provider/model behavior.
8. Remote-client behavior.

Stop at the first failed prerequisite. Later-layer errors may be symptoms.

## Evidence limits

- Historical readiness reports prove only the dated revision and checks shown.
- A public `302` proves the route reaches Authelia, not that authenticated
  application behavior works.
- A running container does not prove its application process or safety
  contract.
- Host success does not prove a remote container can reach the same listener.
