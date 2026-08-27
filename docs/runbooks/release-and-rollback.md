# Release and Rollback

## Purpose

Promote one immutable Switchyard revision through qualification, deployment,
contract verification, and rollback without confusing a healthy container with
a healthy application.

## Release identity

Before qualification, record:

- the reviewed `main` commit and annotated release tag;
- the Gitea pull request and CI run;
- the expected migration set;
- the release archive checksums and image revision labels.

The checkout, tag, CI revision, image labels, and deployment evidence must all
identify the same commit.

Production web transport is host-managed Tailscale. The Tailscale daemon and
central tailnet policy remain outside Compose. Keep their credentials in the
host secret store. Do not add nginx TLS or mTLS, container credentials, or a
plaintext fallback.

## Qualification

Run from a clean checkout on the dedicated production runner:

```bash
make verify-production
make api-soak-extended
```

Do not promote when either command fails. Save the CI run URL, release
checksums, and source revision in `docs/releases/<version>-qualification.md`.
Create candidate-phase JSON evidence using
`docs/releases/release-evidence-v1.schema.json`, then validate the exact
candidate and generated manifest:

```bash
make release-evidence-verify \
  CANDIDATE_SHA="$(git rev-parse HEAD)" \
  EVIDENCE=docs/releases/<version>-evidence.json \
  MANIFEST=release/manifest.json
```

The candidate phase requires the release tag, pull request and CI run
references, migration head, rollback target, explicit exceptions array,
qualification proof, and extended-soak proof, but no deployment fields. Use an
empty exceptions array when none exist; do not use placeholder entries.
Historical matching proof does not expire. Treat revision, release-tag, CI-run,
or artifact mismatches as stale evidence.

## Pre-deployment capture

On the resolved production host, inspect without printing secrets:

```bash
git status --short
git rev-parse HEAD
systemctl cat switchyard.service
systemctl is-active switchyard.service
docker compose ps
docker compose images
ss -ltn
```

Export the deployed `.env` without printing it, then validate the static
production contract:

```bash
set -a
. ./.env
set +a
make production-config-check
```

The check requires `SWITCHYARD_WEB_TRANSPORT=tailscale`, distinct destination and
proxy peers in `100.64.0.0/10`, a private `/24`-or-smaller Docker bridge with a
usable gateway, nginx trust set to that gateway's exact `/32`, an HTTPS public
URL, reverse-proxy authentication, and a hop token. A pass does not prove the
tailnet ACL or target route.

Create and verify a pre-deployment backup:

```bash
SWITCHYARD_BACKUP_DIR=/approved/encrypted/backups \
SWITCHYARD_BACKUP_POLICY=/approved/non-secret/backup-policy.json \
make postgres-backup
SWITCHYARD_RESTORE_BUNDLE=/approved/encrypted/backups/<bundle>.complete.json \
SWITCHYARD_RESTORE_ACTIVE_POLICY=/approved/non-secret/backup-policy.json \
SWITCHYARD_RESTORE_EVIDENCE_DIR=/approved/private/restore-evidence \
SWITCHYARD_RESTORE_TIMEOUT_SECONDS=900 \
make restore-drill
```

Retain the schema-valid evidence basename. Confirm bundle validity against the
archived policy and inspect current-policy qualification separately. The
measured backup age and isolated drill duration do not prove production RPO or
RTO. Record the previous release tag, image IDs, bundle ID/checksum, migration
head, listener owner, and rollback decision-maker without copying absolute
paths or credentials into evidence.

## Deployment

Only after qualification and backup verification, switch the clean production
checkout to the approved tag using the normal non-destructive Git workflow,
then install the nft gate and repository-owned unit from the same canonical env
path. The override must be absolute, regular, and not a symlink. Recreate the
default network when deploying the pinned bridge name, API address, and web address for the
first time:

```bash
sudo systemctl stop switchyard
docker compose --env-file /absolute/path/to/switchyard.env -f docker-compose.yml down
sudo SWITCHYARD_ENV_FILE=/absolute/path/to/switchyard.env ./deploy/install-web-gate.sh
sudo SWITCHYARD_ENV_FILE=/absolute/path/to/switchyard.env ./deploy/install-systemd.sh
```

The gate installer checks Compose and rendered nft syntax before mutation and
installs root-owned mode-0755 renderer and UFW helper scripts under
`/usr/lib/switchyard`. It ensures the exact web-forward and API-to-host-model
UFW rules before Docker; rollback deletes only rules created by that invocation. If gate content changes, it
stops Compose `web` before replacing files or live rules. Failure restores the
prior renderer, rules/table, drop-in, unit, enablement, and active state, but
web stays stopped; correct the failure and rerun both installers. The systemd
installer refuses mutation until the installed gate is active. On main-unit
failure, it restores the previous unit and state, then restores the prior
workload only after rechecking the gate. Inspect both installed units and
confirm every main/gate/Compose `--env-file` argument names the same path.

## Verification

Set the control token without printing it, then run:

```bash
make production-smoke
```

Run this command on the production host. It proves the configured deployment,
systemd and nft state, listener ownership, and revision-bound local paths. Then
dispatch `.gitea/workflows/prove-production-deployment.yml` on the exact annotated
tag. That job proves the remote web health, authenticated control summary, model
ingress, public authentication redirect, and live source/deployment identity. It
does not replace the on-host gate.

Also verify the canonical model route from the affected remote client. Confirm
that:

- control API and PostgreSQL remain loopback-only;
- model ingress cannot access control-plane routes;
- the public route reaches Authelia;
- the web hop uses the approved host-managed Tailscale peers;
- nft counters show the original approved Tailscale peer before DNAT;
- UFW admits only the exact Tailscale peer to the pinned web container address and nginx admits that same peer `/32`;
- the API accepts only the hop token plus authenticated user identity;
- Ollama is reached on its real upstream port.

Do not close Gitea issues #18 or #19 from static validation. Record human and
target-path proof separately.

Update the JSON evidence to the deployed phase. Record the deployed SHA, host,
deployment time, backup/restore proof, and production-smoke proof. Run
`make release-evidence-verify` again before accepting the release.

## Application rollback

Use application-only rollback when the previous release is schema-compatible:

1. Preserve current logs and deployment evidence.
2. Switch the clean checkout to the recorded previous tag.
3. Reload `switchyard.service`.
4. Run `make production-smoke`.
5. Record the deployed revision and listener ownership.

## Database rollback

Database rollback is exceptional:

1. Stop new API writes under an announced maintenance window.
2. Preserve the failed database state and logs.
3. Verify the pre-deployment backup checksum.
4. Restore into an isolated container first.
5. Restore production only after explicit operator approval.
6. Start the known-good application release.
7. Run the full production contract smoke.

Never overwrite the active database merely to test a restore.

## Model-ingress rollback

Follow `native-model-broker-cutover.md`. Never let legacy llama-line and
Switchyard own port `11434` simultaneously.
