# deploy/

## Responsibility
Installs the Docker Compose stack as a host-managed systemd service and defines the pinned Gitea Actions job image used by Switchyard verification.

## Design
- All three Docker-consuming service templates use repository/user/primary-group and Docker supplementary-group placeholders. The Docker group defaults to `docker` and can be set with `SWITCHYARD_DOCKER_GROUP` without changing account memberships.
- `deploy/docker-local` is the only production Docker CLI boundary. It runs `/usr/bin/docker` against `unix:///var/run/docker.sock` with a fresh empty CLI config and a clean environment. Production uses public images or local builds and requires a system-wide Compose plugin; it never loads user registry auth or CLI plugins.
- `deploy/switchyard-compose` resolves one fingerprinted generated file-set record and invokes `docker-local` with exactly the repository base and generated Open Pilot override; mismatched runtime/Compose fingerprints fail before Docker.
- `install-web-gate.sh` uses the canonical Docker boundary while preflighting Compose, then installs the root-owned exact-UFW helper, ensures separate web-forward and API-to-host-model rules before Docker, and removes only rules created by that invocation during rollback.
- `install-systemd.sh` requires the installed web gate active before mutation, resolves one absolute non-symlink env file, and probes the canonical Docker boundary with the rendered runtime identity. It restores all five managed unit files, their enablement/activation, and the Compose workload through that same boundary after a failed install.
- `runner/Dockerfile` composes the digest-pinned Gitea runner image with the exact Go toolchain required by CI; `runner/README.md` documents publishing and label configuration.
- The installer removes a legacy standalone llama-line service during native cutover.
- Backup service/timer templates are installed alongside the runtime unit and enabled only through an explicit opt-in with a configured backup destination.
- Restore-drill service/timer templates are rendered from the validated policy calendar and bounded RTO. They are enabled only with `SWITCHYARD_INSTALL_RESTORE_DRILL_TIMER=1`; all probes precede mutation, and deadline initialization occurs immediately before timer enable with rollback on failure.
- Starter monitoring rules cover canonical model-ingress availability, broker
  backlog, worker failures, and backup freshness. The API republishes the
  backup and restore-drill protected textfiles on `/metrics`; external rules alert on a failed or overdue drill.

## Flow
1. Resolve the production env path, install the root-owned renderer and UFW helper, ensure the exact post-DNAT route allow, and render/install `/etc/switchyard/web-gate.nft` and `switchyard-web-gate.service`; a changed live gate first stops Compose `web`.
2. Require the gate active, then resolve repository and run identity and install `switchyard.service`.
3. Disable legacy llama-line if present.
4. Preflight restore policy, private directories, Docker, and policy calendar when restore scheduling is explicitly requested.
5. Install backup and restore-drill templates; enable each timer only through its own opt-in.
6. Reload systemd, enable/start Switchyard, and print status.

## Integration
Every production Compose call pins the root `docker-compose.yml` and the installed external env file.
