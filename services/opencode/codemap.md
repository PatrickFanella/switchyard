# services/opencode/

## Responsibility
Builds the managed OpenCode server image used as Switchyard's agent execution service.

## Design
- A pinned Node/Alpine image installs OpenCode 2.0.22, RTK, Git, Go, and build tooling.
- In strict mode, `install.sh` removes unmanaged extension trees, replaces persisted configuration without reading it, and atomically sanitizes persistent `auth.json` to mode `0600`, retaining only valid API/OAuth provider credentials. The session DB is in memory; the native context hook saves refreshed provider credentials before dispatch so they survive restart. Development mode leaves auth state untouched, may merge reviewed local configuration, and may use persistent state.
- Persistent OpenCode config/data are pre-owned bind mounts; startup fails closed instead of recursively changing host ownership.

## Flow
1. The image installs locked dependencies and a checksum-verified RTK installer.
2. Container startup runs `switchyard-opencode-install`.
3. Managed defaults are copied/merged into the mounted config directory.
4. The managed server wrapper starts `opencode serve` on port 4096 and restores retained provider credentials through the native credential API. Empty in-memory database bootstrap does not run the upstream legacy-credential migration. Health waits for credential restoration; native OAuth method and token-sharing metadata survive refresh/restart.
5. The Go executor creates sessions and admits prompts through the native `/api` contract.

## Integration
- Health-checked and networked with the API in `docker-compose.yml`.
- Shares `/workspace` with the API for isolated Open Pilot worktree operations.
- `backend/internal/controlplane/ocq_executor.go` is the primary consumer.
