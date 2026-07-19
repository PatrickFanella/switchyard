# backend/internal/app/

## Responsibility
Provides the small startup greeting used by the server process.

## Design
`Greeting` trims input and supplies a safe default; it has no runtime state or dependencies beyond the standard library.

## Flow
`cmd/app` passes the product name to `Greeting` and prints the returned startup string.

## Integration
Consumed only by `backend/cmd/app/main.go`.
