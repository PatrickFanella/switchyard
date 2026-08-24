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

Leave `retirement_proposal` absent for ordinary releases. When a follow-on
deprecated-selector removal is explicitly proposed, the manual workflow also
requires one Gitea run/artifact reference for `compatibility-retirement-report.json`
and a JSON array of exactly two prior qualifying release records. The workflow
downloads the bounded report with its built-in token and records both its exact
raw-byte SHA-256 and its key-order-independent canonical SHA-256. Validate that
artifact with:

```bash
make release-evidence-verify \
  CANDIDATE_SHA="$(git rev-parse HEAD)" \
  EVIDENCE=release/candidate-evidence.json \
  MANIFEST=release/manifest.json \
  RETIREMENT_REPORT=release/compatibility-retirement-report.json \
  ARTIFACT_ROOT=release
```

The report must contain 90 elapsed, complete UTC days for both `llama-line` and
`ocq`, all with zero attempts. Candidate evidence binds the report's source
revision and deployment identity without treating the report source as a prior
qualifying release.
The report reference and each prior-release input record contain only positive
`run_id` and `artifact_id` integers for artifacts in the same Gitea repository.
Exactly two prior-release records are required;
`retirement-prior-references-v1.schema.json` documents that input. The workflow
uses its built-in token to verify successful run status, `workflow_id`, `url`,
head SHA, production-runner job, exact artifact name, expiry, and run membership
from Gitea 1.24 API fields before download. It accepts only an exact four-file bundle containing
`retirement-release-qualification.json` and the three visible JSON basenames named
by that qualification. The workflow copies the original bytes below
`release/prior-qualifying-releases/<index>/`; it does not recreate historical trust
from caller-supplied timestamps. Prior deployed evidence containing its own
`retirement_proposal` is invalid; the validator
does not silently remove it. The validator loads all files beneath
`ARTIFACT_ROOT`. It rejects raw-byte reformatting, path escapes, cycles, missing
deployment gates, and mismatched revision, tag, CI, migration, archive, or checksum
bindings. It never reconstructs a manifest from evidence. The candidate report
link also resolves beneath `ARTIFACT_ROOT`; its canonical digest and source
revision, deployment identity, and segment must match the supplied report.
Prior report and evidence chronology is checked against that prior release's
own `recorded_at` and `qualified_at`, not the candidate's timestamps.
The candidate tag (with or without `v`) or SHA cannot be reused. This evidence
permits a separate removal proposal; it does not remove or disable selectors in
the qualifying release.

After a release has accumulated a complete 90-day report, manually dispatch
`switchyard-retirement-release-qualification` on the production runner. This
opt-in workflow checks out the annotated deployed tag, reruns the authenticated
production smoke, validates a scheduler-created restore record from the private
production evidence directory, downloads the exact two-file candidate record,
and fetches the report from the authenticated control API. The checkout,
candidate record, manifest, current migration, report revision, and deployment
identity must agree. The deployment and its original smoke must predate the
report window. Missing production secrets, evidence paths, or proof fail the
run.
The historical smoke URL must name a successful run of
`.gitea/workflows/prove-production-deployment.yml` for the deployed SHA. Dispatch
that proof workflow immediately after the external deployment. The run must
contain exactly one successful `production-smoke` job carrying the
`switchyard-production` runner label; its authenticated start and completion
timestamps bind the deployment evidence chronology.

The resulting artifact is retained for 400 days and contains only
`retirement-release-qualification.json`, `deployed-evidence.json`,
`manifest.json`, and `compatibility-retirement-report.json`. The producer binds
the exact bytes with raw SHA-256 values and binds the parsed report with its
canonical SHA-256. `download_retirement_qualifications.py` rejects every other
filename. This machine qualification does not supply the human or external
retirement approval tracked in issue #14. Keep that proof boundary open until a
human records the external decision.

The pull request must be a credential-free HTTPS repository URL ending in
`/pulls/<positive integer>`. The rollback target must be either a full lowercase
Git SHA or a semver-like release tag, and it must differ from both the candidate
SHA and release tag. JSON evidence and manifests must not contain duplicate
keys. Candidate workflow artifacts contain the evidence, manifest, checksums,
and the exact `release/*.oci.tar` archives named by the manifest.

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
