# Release qualification records

Keep the historical Markdown records. For new qualifications, also create a JSON
record that follows `release-evidence-v1.schema.json`. The validator uses only
Python's standard library and local files. It does not read secrets or contact
Gitea, a registry, or a production host.

Use `phase: "candidate"` after qualification. It requires the candidate SHA,
release tag, pull request reference, CI run reference, migration head, rollback
target, an explicit exceptions array, `verify_production` and `extended_soak`
proof, and every archive checksum from the generated `release/manifest.json`.
The exceptions array may be empty; each entry must contain real, non-placeholder
text. Candidate records do not require deployment fields.

The manual `switchyard-production-verify` dispatch generates the candidate
record. Run it against the annotated release tag and supply the reviewed pull
request URL and immutable rollback target. Scheduled qualification runs test the
contract but do not publish a release candidate record.

After promotion, change the phase to `deployed`. Add `deployment`,
`backup_restore`, and `production_smoke` proof. The deployed SHA must equal the
candidate SHA. Historical records remain valid indefinitely when the candidate,
evidence, manifest revision, release tag, CI proof, and artifact checksums still
match. Revision or proof mismatches make evidence stale; age alone does not.

Validate a record with explicit inputs:

```bash
make release-evidence-verify \
  CANDIDATE_SHA="$(git rev-parse HEAD)" \
  EVIDENCE=docs/releases/v0.1.3-evidence.json \
  MANIFEST=release/manifest.json
```

The JSON record complements, rather than replaces, one Markdown file per
promoted release. Check in both records after deployment. Ordinary `make verify`
tests the validator and schema without loading historical records or ignored
files from `release/`. Each Markdown record must name:

- release tag and exact Git SHA;
- pull request and CI run IDs;
- `make verify-production` and extended-soak results;
- image revision labels, archive checksums, and migration version;
- pre-deployment backup and restore-drill evidence;
- production host and deployment timestamp;
- control API, model ingress, remote caller, isolation, public web, and proxy
  boundary results;
- rollback target and unresolved exceptions.

Historical readiness reports prove only the revision and date they name.

For a release that claims alert delivery, run `make alert-lifecycle` manually
against the deployed source revision as described in the incident runbook.
Retain its redacted JSON evidence with the release records. Baseline
`make verify` runs the harness's fake-server tests only and does not contact
Alertmanager or ntfy. The lifecycle evidence proves firing and recovery
delivery, not human acknowledgement.
