# Runbooks

Runbooks describe repeatable procedures that an operator or agent can follow safely.

Good runbooks include:

- Purpose
- Preconditions
- Commands
- Expected output
- Rollback or cleanup steps
- Verification

Add Switchyard-specific runbooks as operational procedures stabilize.

- [Native Model Broker Cutover](native-model-broker-cutover.md) — operate and troubleshoot Switchyard on the llama-line-compatible `11434` ingress.
- [Release and Rollback](release-and-rollback.md) — qualify, deploy, verify, and roll back one immutable revision.
- [PostgreSQL Backup and Restore](postgres-backup-and-restore.md) — create verified backups and exercise isolated restoration.
- [Incident Response](incident-response.md) — diagnose by contract layer and operate initial alerts.
- [Artifact and Compatibility Lifecycle](artifact-and-compatibility-lifecycle.md) — operate inline artifact retention and retire compatibility routes from durable evidence.
