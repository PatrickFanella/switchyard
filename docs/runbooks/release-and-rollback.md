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

Create and verify a pre-deployment backup:

```bash
SWITCHYARD_BACKUP_DIR=/approved/encrypted/path make postgres-backup
BACKUP=/approved/encrypted/path/<file>.dump make postgres-restore-drill
```

Record the previous release tag, image IDs, backup path/checksum, migration
version, listener owner, and rollback decision-maker.

## Deployment

Only after qualification and backup verification, switch the clean production
checkout to the approved tag using the normal non-destructive Git workflow,
then use the installed repository-owned unit:

```bash
sudo systemctl reload switchyard
```

Inspect the installed unit before relying on the repository template.

## Verification

Set the control token without printing it, then run:

```bash
make production-smoke
```

Also verify the canonical model route from the affected remote client. Confirm
that:

- control API and PostgreSQL remain loopback-only;
- model ingress cannot access control-plane routes;
- the public route reaches Authelia;
- direct dashboard access is denied except from the approved proxy;
- Ollama is reached on its real upstream port.

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
