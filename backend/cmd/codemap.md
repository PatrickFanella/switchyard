# backend/cmd/

## Responsibility
Contains executable entry points for the Switchyard server, isolated verifier, and operator CLI.

## Design
Entry points remain thin: server composition lives in `cmd/app`, verifier request execution in `cmd/verifier`, and CLI transport and presentation in `cmd/switchyard`; domain behavior stays in `internal/controlplane`. The verifier keeps direct Git fd-bound to a validated identity, runs other commands in bounded private snapshots, binds results to source manifests, and reaps escaped children between serialized requests.

## Flow
1. A built binary receives environment configuration or command-line flags.
2. The server composes runtime services; the CLI sends authenticated HTTP requests.
3. Exit status and structured logs/output report the result.

## Integration
- `Makefile` builds both commands into ignored `bin/` artifacts.
- Both consume contracts owned by `internal/controlplane`.
