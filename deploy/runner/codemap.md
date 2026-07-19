# deploy/runner/

## Responsibility
Defines the reproducible Gitea Actions job image used by Switchyard verification workflows.

## Design
- Layers the exact Go toolchain over a digest-pinned Gitea runner base image.
- Validates Go, Node/Corepack, Docker/Compose/Buildx, and Make during image construction.
- Requires publication and runner-label configuration by immutable manifest digest.

## Flow
1. Build and push `Dockerfile` to a registry reachable by the runner daemon.
2. Configure the `switchyard-production` runner label with the published digest.
3. Gitea workflows validate the runtime contract before invoking Make gates.

## Integration
- Consumed by `.gitea/workflows/template-check.yml` and `.gitea/workflows/verify-production.yml` through the runner label.
- Publishing and label configuration are documented in `README.md`.
