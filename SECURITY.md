# Security Policy

## Supported scope

This repository is the Switchyard project. Security work here focuses on safe defaults, secret hygiene, and project-specific guidance.

Switchyard separates its operator control plane from model-compatible ingress. Production deployments must keep raw listeners private and place the web/control surface behind authenticated access. Setting `SWITCHYARD_REVERSE_PROXY_AUTH=true` is an operator assertion, not an authentication mechanism by itself.

Production web traffic may cross the host boundary only through host-managed Tailscale. Set `SWITCHYARD_WEB_TRANSPORT=tailscale`, bind the web listener to this host's `100.64.0.0/10` address, and set `SWITCHYARD_WEB_TRUSTED_PROXY_CIDR` to the other peer's distinct Tailscale `/32`. The nginx gateway denies other sources and clears client-supplied `X-Forwarded-User`. Do not broaden the allowlist, add a plaintext LAN fallback, terminate TLS or mTLS in nginx, or put Tailscale credentials in Compose. The host Tailscale daemon and central tailnet policy are external security controls. Store their credentials as host-managed secrets outside this repository.

Run `make production-config-check` with the deployed environment before the target smoke. This static check does not prove the Tailscale ACL, route, public authentication redirect, or target listener ownership.

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

Switchyard should:

- commit lockfiles for application projects when the stack expects them
- pin container base images deliberately
- document update procedures
- run dependency and container scans where appropriate
- keep generated caches out of git

## Open Pilot safety

Open Pilot tasks should include a deterministic `Test Command` and clear scope limits. Do not queue tasks that require secret access, production credentials, destructive data changes, or ambiguous deployment actions.

For risky work, require human review before adding `agent:queued`.

`OPEN_PILOT_COMMAND_POLICY_MODE` defaults to `deny`. The `allow` mode still enforces executable and Git-subcommand allowlists and rejects shell syntax. Do not weaken these controls on a listener reachable by untrusted clients.
