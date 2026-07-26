# Release Qualification Records

Create one file per promoted release. Each record must name:

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
