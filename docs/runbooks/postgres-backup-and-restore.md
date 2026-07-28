# PostgreSQL Backup and Restore

## Policy

The initial operating policy is:

- daily encrypted custom-format backups;
- an alert when the newest successful backup is older than 26 hours;
- 35 daily and 12 monthly copies, subject to owner approval;
- a monthly isolated restore drill;
- a verified pre-deployment backup retained through the rollback window.

The approved storage owner, encryption-key owner, recovery point objective, and
recovery time objective must be recorded outside secrets-bearing configuration.

## Create a backup

Set an access-controlled destination and run:

```bash
SWITCHYARD_BACKUP_DIR=/approved/encrypted/path make postgres-backup
```

The command:

- uses `pg_dump -Fc` inside the pinned PostgreSQL service;
- writes through a private temporary file;
- verifies the archive with `pg_restore --list`;
- publishes it atomically;
- writes a SHA-256 and source-revision metadata file;
- never prints database credentials.

When `SWITCHYARD_BACKUP_METRICS_FILE` is set, the command also atomically writes
the successful backup timestamp in Prometheus text format. Set
`SWITCHYARD_BACKUP_METRICS_DIR` to a dedicated directory containing only that
file, and set the file path to
`${SWITCHYARD_BACKUP_METRICS_DIR}/backup.prom`. Compose mounts the directory
read-only into the API so atomic replacement remains visible across backup
runs, then republishes the timestamp on `/metrics`. Do not place backup archives
in the metrics directory. Create one successful backup before reloading Compose
with non-empty metrics paths.

Backups must live outside the Docker volume and repository.

## Restore drill

Run against a real retained backup:

```bash
BACKUP=/approved/encrypted/path/switchyard-....dump make postgres-restore-drill
```

The drill:

- verifies metadata when present;
- creates an isolated PostgreSQL 17 container;
- restores without production ownership or privileges;
- checks critical tables and migration history;
- starts a temporary Switchyard API against the restored database;
- verifies readiness and an authenticated workflow request;
- removes only temporary drill resources.

Record backup age, checksum, source revision, migration count, duration, and
whether the measured result met the approved RPO/RTO.

## Installing the daily timer

Put `SWITCHYARD_BACKUP_DIR` in the production `.env`, then explicitly opt in:

```bash
sudo SWITCHYARD_INSTALL_BACKUP_TIMER=1 ./deploy/install-systemd.sh
systemctl status switchyard-backup.timer
```

The installer refuses to enable the timer when the backup destination is not
configured.

## Restore incident

Do not restore over production during diagnosis. First:

1. preserve the failing state and logs;
2. verify the chosen backup checksum;
3. complete an isolated restore drill;
4. obtain explicit authorization for the maintenance window;
5. follow `release-and-rollback.md`;
6. verify application, authentication, model ingress, public web, and listener
   ownership after restoration.
