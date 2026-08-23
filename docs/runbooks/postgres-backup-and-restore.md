# PostgreSQL backup and restore

## Operator policy

Every backup requires an operator-supplied policy that passes
`docs/persistence/operator-policy-v1.schema.json` and the stricter standard
library validator. The policy must state all of these values:

- policy ID and integer version;
- the `postgres` provider;
- storage and encryption owners, plus the encryption expectation;
- retention duration in `P<n>D` form;
- declared RPO and RTO in seconds;
- drill calendar, maximum drill interval, and alert grace.

The validator supplies no defaults. It rejects extra fields, duplicate JSON
keys, placeholders, secret-shaped values, absolute paths, and alternate
providers. Keep the immutable non-secret policy outside the repository. Policy
retention is a declaration; the backup command does not prune files.

The checked-in contracts are:

- `docs/persistence/operator-policy-v1.schema.json`;
- `docs/persistence/postgres-object-set-v1.json`;
- `docs/persistence/postgres-restore-payload-v1.schema.json`;
- `docs/persistence/completed-backup-bundle-v1.schema.json`;
- `docs/persistence/restore-evidence-v1.schema.json`.

Validate a policy before changing a database:

```bash
python3 scripts/restore_evidence.py validate-policy \
  --policy /approved/non-secret/switchyard-backup-policy.json
```

The command prints only the policy SHA-256 digest.

## Create a completed backup bundle

Set a private destination and the validated policy path:

```bash
SWITCHYARD_BACKUP_DIR=/approved/encrypted/backups \
SWITCHYARD_BACKUP_POLICY=/approved/non-secret/switchyard-backup-policy.json \
make postgres-backup
```

The command captures the full source revision, PostgreSQL server version, exact
pre-backup migration ledger and head, object-contract digest, policy digest,
and archive SHA-256. It publishes five owner-only files in one directory:

1. the custom-format `.dump` archive;
2. an immutable `.policy.json` snapshot;
3. the versioned `.object-contract.json` snapshot used by that backup;
4. `.metadata.json`;
5. `.complete.json`, written last as the completion marker.

Publication is no-clobber, symlink-safe, and atomic at each filename. Consumers
must ignore archives without a valid completion manifest. The command prints
only bundle IDs, basenames, and the archive digest. It never prints credentials
or absolute paths.

When `SWITCHYARD_BACKUP_METRICS_FILE` is set, the command updates the existing
successful-backup Prometheus textfile only after the completion manifest exists.
Keep `SWITCHYARD_BACKUP_METRICS_DIR` separate from backups and set the file path
to `${SWITCHYARD_BACKUP_METRICS_DIR}/backup.prom`. Compose mounts that directory
read-only into the API. Create one completed backup before reloading Compose
with non-empty metrics paths.

Backups must live outside the Docker volume and repository. The command neither
implements retention pruning nor supports a second provider.

## Run a disposable restore drill

Use the completed manifest, not the dump:

```bash
SWITCHYARD_RESTORE_BUNDLE=/approved/encrypted/backups/switchyard-....complete.json \
SWITCHYARD_RESTORE_ACTIVE_POLICY=/approved/non-secret/switchyard-backup-policy.json \
SWITCHYARD_RESTORE_EVIDENCE_DIR=/approved/private/restore-evidence \
SWITCHYARD_RESTORE_TIMEOUT_SECONDS=900 \
make restore-drill
```

`make postgres-restore-drill` remains the PostgreSQL-default compatibility
entrypoint and requires the same environment.

The provider-neutral wrapper validates the completed bundle only against its
archived policy snapshot. It separately compares the active policy digest and
measures backup age against the active declared RPO. A retained old-policy
bundle remains restorable, but its evidence sets current-policy qualification
to false.

The sole PostgreSQL adapter:

- accepts no database target;
- creates a unique internal Docker network and publishes no database or API host ports;
- generates a cryptographically random database password and API token for each attempt;
- labels every lock, network, database, and API resource with its exact owner, type,
  attempt, creation time, and reclaim time;
- runs under a hard timeout and removes only resources whose complete ownership labels match;
- blocks on fresh resources, but reclaims stale resources even when Docker still reports
  them running; stale means older than the active policy RTO plus a fixed 60-second cleanup grace;
- restores without archived ownership or privileges;
- checks the exact required public object set;
- checks the exact archived migration ledger and head before API startup;
- records each nonnegative table row count as an observation, not integrity proof;
- starts the API only after preserving pre-start evidence;
- records the post-start migration ledger, readiness, and authenticated workflow check.

The API reaches PostgreSQL by container DNS. Readiness and authenticated probes run
inside the isolated API container. Credentials are never printed or added to evidence.

Every attempt writes one unique owner-only JSON evidence file with a bounded
stage and sanitized error code. Evidence contains no SQL, response bodies,
credentials, or absolute paths. The RTO comparison is labeled as isolated drill
duration. Neither the RPO nor RTO result proves production incident recovery.

## Installing the daily backup timer

Put both `SWITCHYARD_BACKUP_DIR` and `SWITCHYARD_BACKUP_POLICY` in the production
`.env`, then explicitly opt in:

```bash
sudo SWITCHYARD_INSTALL_BACKUP_TIMER=1 ./deploy/install-systemd.sh
systemctl status switchyard-backup.timer
```

The installer schedules backups only. Restore-drill scheduling and restore
metrics belong to Gitea #24 and are not implemented.

## Restore incident

Do not restore over production during diagnosis. First:

1. Preserve the failing state and logs.
2. Validate the selected completion manifest and archived policy binding.
3. Complete an isolated restore drill.
4. Obtain explicit authorization for the maintenance window.
5. Follow `release-and-rollback.md`.
6. Verify application, authentication, model ingress, public web, and listener ownership after restoration.
