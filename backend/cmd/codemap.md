# backend/cmd/

## Responsibility
Contains the executable entry points for the Switchyard server and operator CLI.

## Design
Entry points remain thin: server composition lives in `cmd/app`, while CLI transport and presentation live in `cmd/switchyard`; domain behavior stays in `internal/controlplane`.

## Flow
1. A built binary receives environment configuration or command-line flags.
2. The server composes runtime services; the CLI sends authenticated HTTP requests.
3. Exit status and structured logs/output report the result.

## Integration
- `Makefile` builds both commands into ignored `bin/` artifacts.
- Both consume contracts owned by `internal/controlplane`.
