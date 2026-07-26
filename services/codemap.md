# services/

## Responsibility
Contains managed runtime services that Switchyard composes alongside its native Go control plane.

## Design
The current service boundary is the managed OpenCode execution environment; generic scheduling and workflow mechanics remain in the Go runtime.

## Flow
Docker Compose starts the service, installs Switchyard defaults, and exposes it only to the API/runtime that creates sessions and submits prompts.

## Integration
- Built by `docker-compose.yml` and release Make targets.
- Called through `backend/internal/controlplane`'s `OpenCodeExecutor` contract.
