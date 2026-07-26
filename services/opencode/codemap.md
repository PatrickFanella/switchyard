# services/opencode/

## Responsibility
Builds the managed OpenCode server image used as Switchyard's agent execution service.

## Design
- A pinned Node/Alpine image installs OpenCode, RTK, Git, Go, and build tooling.
- `install.sh` copies managed plugins/agents/skills and merge-preserves user configuration.
- Persistent OpenCode config/data and a shared workspace are mounted at runtime.

## Flow
1. The image installs locked dependencies and a checksum-verified RTK installer.
2. Container startup runs `switchyard-opencode-install`.
3. Managed defaults are copied/merged into the mounted config directory.
4. `opencode serve` starts on port 4096; the Go executor creates sessions and sends messages.

## Integration
- Health-checked and networked with the API in `docker-compose.yml`.
- Shares `/workspace` with the API for isolated Open Pilot worktree operations.
- `backend/internal/controlplane/ocq_executor.go` is the primary consumer.
