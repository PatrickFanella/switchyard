# Security Policy

## Supported scope

This repository is a public base template. Security work here focuses on safe defaults, secret hygiene, and guidance for derived projects.

Derived repositories should add stack-specific security policy details before production use.

## Reporting a vulnerability

Do not publish sensitive vulnerabilities in public issues.

Report privately to the repository owner or organization maintainer through the preferred private channel for the project. If no project-specific channel exists, contact the maintainer account for the Gitea organization.

Include:

- affected repository and commit
- impact summary
- reproduction steps
- affected configuration or deployment path
- suggested mitigation, if known

## Secret handling

Never commit:

- `.env` or `.env.*` files, except `.env.example`
- API tokens or OAuth secrets
- SSH keys or deploy keys
- database dumps containing real data
- private certificates or signing keys
- webhook secrets

Use placeholders in docs and examples. Report only secret key names and presence, never secret values.

## Dependency and supply-chain hygiene

Derived templates should:

- commit lockfiles for application projects when the stack expects them
- pin container base images deliberately
- document update procedures
- run dependency and container scans where appropriate
- keep generated caches out of git

## Open Pilot safety

Open Pilot tasks should include a deterministic `Test Command` and clear scope limits. Do not queue tasks that require secret access, production credentials, destructive data changes, or ambiguous deployment actions.

For risky work, require human review before adding `agent:queued`.
