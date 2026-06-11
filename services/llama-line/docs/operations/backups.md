# llama-line Backups

## Local Postgres

Create a dump:

```bash
scripts/backup_postgres.sh
```

Restore a dump:

```bash
scripts/restore_postgres.sh backups/postgres/llama_line-YYYYMMDDTHHMMSSZ.sql.gz
```

The current local dump directory is `backups/postgres/` and is gitignored.

## Homelab backup follow-up

For durable backups, add `/home/onnwee/Projects/tools/llama-line/backups/postgres` or an equivalent scripted dump path to the relevant host backup runbook. Do not back up raw prompt/response bodies without accepting privacy risk.

## Phoenix data

Phoenix still stores SQLite data in almaz Docker volume `monitoring_phoenix_data`. That is separate from llama-line Postgres and needs either a volume backup or a Phoenix migration to Postgres.
