# Security Policy

## Supported scope

This repository is the Switchyard project. Security work here focuses on safe defaults, secret hygiene, and project-specific guidance.

Switchyard separates its operator control plane from model-compatible ingress. Production deployments must keep raw listeners private and place the web/control surface behind authenticated access. Setting `SWITCHYARD_REVERSE_PROXY_AUTH=true` is an operator assertion, not an authentication mechanism by itself.

Production web traffic may cross the host boundary only through host-managed Tailscale. Set `SWITCHYARD_WEB_TRANSPORT=tailscale`, bind the web listener to this host's `100.64.0.0/10` address, and set `SWITCHYARD_WEB_TRUSTED_PROXY_CIDR` to the other peer's distinct Tailscale `/32`. The nginx gateway denies other sources and clears client-supplied `X-Forwarded-User`. Do not broaden the allowlist, add a plaintext LAN fallback, terminate TLS or mTLS in nginx, or put Tailscale credentials in Compose. The host Tailscale daemon and central tailnet policy are external security controls. Store their credentials as host-managed secrets outside this repository.

Run `make production-config-check` with the deployed environment before the target smoke. This static check does not prove the Tailscale ACL, route, public authentication redirect, or target listener ownership.

Production must run OpenCode in `strict` mode with a non-default password and a
dedicated absolute Git top-level host workspace outside this repository. Only
the API mounts the full workspace. OpenCode sees the Git common directory
read-only and the managed worktree subtree read-write; the verifier sees both
read-only. Neither mounts the source checkout. `GIT_OPTIONAL_LOCKS=0`, strict
command policy, and managed-worktree identity checks supplement the read-only
mounts. The persistent executor can still see sibling managed worktrees. Use
non-root numeric UID/GID values matching the private, non-symlink workspace
layout and marker. For local development, run `make workspace-init`; it only
initializes an empty default relative workspace and never changes host
ownership. Production config/data bind mounts must also be canonical dedicated
directories with matching runtime ownership and safe modes. The container
entrypoint refuses mismatches and never recursively changes host ownership.
Strict startup disables project and Claude prompt discovery, rejects runtime
config/permission override variables, replaces the
persisted top-level config, and removes unmanaged plugin, agent, command, mode,
and skill files. The runtime uses only managed `/home/opencode` XDG discovery
paths; no host home or global OpenCode config is mounted.
Strict startup also rejects `OPENCODE_AUTH_CONTENT` and atomically sanitizes
persistent `auth.json` to mode `0600`. Only structurally valid API and OAuth
provider credentials are retained; well-known, unknown, and unrelated account
state is removed without logging credential values. Malformed, oversized,
symlinked, or otherwise unsafe auth state fails startup closed. Development
mode does not alter auth state.

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

Managed OpenCode strict mode denies shell execution entirely, uses `OPENCODE_DB=:memory:`, and never reads persisted configuration before replacing managed surfaces. Restarting it loses the OpenCode session/account DB; Switchyard creates replacement sessions. Agents edit through file tools; approved test commands run only in the internal-network verifier. Direct Git remains fd-bound to the validated read-only worktree. Other commands receive a bounded private writable `/tmp` snapshot with no injected Git identity environment; symlinks and special files are rejected. The verifier caps processes, memory, CPU, concurrent requests, combined output, snapshot files, and snapshot bytes. It contains Git, pinned Go 1.26, Make, npm, pnpm 10.33.0, and Cargo for bounded dependency-free or cached checks; it cannot install arbitrary dependencies. A tool run from the snapshot can spawn its own Git process, which discovers the copied linked `.git` file and original read-only common directory; command policy must not rely on this path for Git mutation. `OPENCODE_SECURITY_MODE=development` may restore a persisted DB, local user permissions, and project config discovery and must never be used for production autonomy.

The verifier serializes requests, binds each result to a SHA-256 manifest of the source file bytes, paths, and Git executable modes, and requires the same manifest before commit. It kills and reaps all remaining request processes, including escaped process groups.

Open Pilot push and remote probes require an HTTPS `origin` matching the
configured Gitea host and requested repository even when no token is present.
The local bare-remote test override is forbidden in production. Managed Git
commands disable hooks, signing, external command helpers, terminal prompts,
protocol extensions, redirecting config, and TLS verification weakening.
