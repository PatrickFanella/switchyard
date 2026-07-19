# backend/cmd/switchyard/

## Responsibility
Provides the `switchyard` operator CLI for Open Pilot runs/cancellation and model catalog operations.

## Design
Uses the standard `flag` and `net/http` packages, supports human or JSON output, and reads control-plane authorization from the environment.

## Flow
1. Parse `open-pilot` or `models` subcommands.
2. Send GET/POST requests to the configured control-plane URL.
3. For attached Open Pilot runs, poll run detail until terminal status or timeout.
4. Render concise output or pass through JSON.

## Integration
- Calls `/api/workflows/open-pilot/run`, run cancellation, and model catalog/pull endpoints.
- `models refresh-opencode` invokes the local `opencode` executable directly.
