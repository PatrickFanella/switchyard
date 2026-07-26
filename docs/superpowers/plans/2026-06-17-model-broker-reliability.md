# Model Broker Reliability Implementation Plan

> **Historical plan:** The bounded retry, status, ledger, and recovery work was subsequently implemented. Use current code, runbooks, and release evidence for status.

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Make Switchyard's native model broker more resilient to upstream Ollama/OpenCode/compat failures with bounded retries, clearer status, and safer async recovery.

**Architecture:** Add a small reliability policy layer inside `EnvModelBroker` rather than introducing a new service. Keep request IDs stable across attempts, classify retryable errors, expose degraded health state, and replace simplistic async restart handling with claim-aware execution metadata where possible.

**Tech Stack:** Go HTTP clients, existing Store ledger, environment config, unit tests.

---

## Approved Scope

- Add bounded retry/backoff for retryable upstream failures.
- Preserve no-retry behavior for canceled contexts and client-side validation errors.
- Expose health status as `healthy`, `degraded`, `unreachable`, or `failed` where applicable.
- Improve async recovery so stale `running` rows are not blindly failed without age/context checks.
- Do not add a separate queue service.

## File Structure Target

- Modify: `backend/internal/controlplane/model_executor.go` — retry policy and attempt wrappers.
- Modify: `backend/internal/controlplane/model_broker_status.go` — degraded status semantics.
- Modify: `backend/internal/controlplane/model_broker_ledger.go` — optional attempt metadata types if needed.
- Modify: `backend/internal/controlplane/llama_line_compat_tracker.go` — safer recovery threshold.
- Modify: `backend/internal/controlplane/model_executor_test.go` and `http_test.go` — retry/recovery tests.
- Modify: `.env.example` and `README.md` — reliability env docs.

## Task 1: Add Retry Policy Parsing

**Files:**
- Modify: `backend/internal/controlplane/model_executor.go`
- Test: `backend/internal/controlplane/model_executor_test.go`

- [ ] **Step 1: Add policy helpers**

Add near `modelBrokerTimeout()`:

```go
type modelBrokerRetryPolicy struct {
    Attempts int
    Backoff  time.Duration
}

func modelBrokerRetryPolicyFromEnv() modelBrokerRetryPolicy {
    attempts := 1
    if raw := strings.TrimSpace(os.Getenv("MODEL_BROKER_RETRY_ATTEMPTS")); raw != "" {
        if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 5 {
            attempts = parsed
        }
    }
    backoff := 250 * time.Millisecond
    if raw := strings.TrimSpace(os.Getenv("MODEL_BROKER_RETRY_BACKOFF")); raw != "" {
        if parsed, err := time.ParseDuration(raw); err == nil && parsed >= 0 && parsed <= 10*time.Second {
            backoff = parsed
        }
    }
    return modelBrokerRetryPolicy{Attempts: attempts, Backoff: backoff}
}

func modelBrokerRetryableStatus(status int) bool {
    return status == http.StatusTooManyRequests || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout || status >= 500
}

func modelBrokerRetryableError(err error) bool {
    if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return false
    }
    return true
}
```

Also add `strconv` to imports.

- [ ] **Step 2: Add tests for parsing**

In `model_executor_test.go`, add:

```go
func TestModelBrokerRetryPolicyFromEnv(t *testing.T) {
    t.Setenv("MODEL_BROKER_RETRY_ATTEMPTS", "3")
    t.Setenv("MODEL_BROKER_RETRY_BACKOFF", "1ms")
    policy := modelBrokerRetryPolicyFromEnv()
    require.Equal(t, 3, policy.Attempts)
    require.Equal(t, time.Millisecond, policy.Backoff)
}
```

- [ ] **Step 3: Run targeted tests**

Run: `go test ./internal/controlplane -run TestModelBrokerRetryPolicyFromEnv`

Expected: passes.

## Task 2: Wrap Upstream Calls With Attempts

**Files:**
- Modify: `backend/internal/controlplane/model_executor.go`
- Test: `backend/internal/controlplane/model_executor_test.go`

- [ ] **Step 1: Add retry wrapper**

Add:

```go
func (e *EnvModelBroker) generateWithRetry(ctx context.Context, backend string, request ModelRequest) (ModelResponse, error) {
    policy := modelBrokerRetryPolicyFromEnv()
    var lastErr error
    for attempt := 1; attempt <= policy.Attempts; attempt++ {
        response, err := e.generateOnce(ctx, backend, request)
        if err == nil {
            if response.CorrelationIDs == nil { response.CorrelationIDs = map[string]string{} }
            response.CorrelationIDs["model_broker_attempts"] = fmt.Sprintf("%d", attempt)
            return response, nil
        }
        lastErr = err
        if !modelBrokerRetryableError(err) || attempt == policy.Attempts {
            break
        }
        timer := time.NewTimer(policy.Backoff * time.Duration(attempt))
        select {
        case <-ctx.Done():
            timer.Stop()
            return ModelResponse{}, ctx.Err()
        case <-timer.C:
        }
    }
    return ModelResponse{}, lastErr
}

func (e *EnvModelBroker) generateOnce(ctx context.Context, backend string, request ModelRequest) (ModelResponse, error) {
    switch backend {
    case "llama-line":
        if strings.TrimSpace(e.llamaLineBaseURL) == "" { return ModelResponse{}, fmt.Errorf("LLAMA_LINE_BASE_URL is not configured") }
        return e.generateViaLlamaLine(ctx, request)
    case "ollama":
        return e.generateViaOllama(ctx, request)
    default:
        return e.generateViaOpenCode(ctx, request)
    }
}
```

Then replace the backend switch in `Generate` with:

```go
return e.generateWithRetry(ctx, backend, request)
```

- [ ] **Step 2: Make HTTP status errors retry-aware**

Where `generateViaOllama`, `generateViaLlamaLine`, or `generateViaOpenCode` handle non-2xx responses, return errors that include status code and can be classified. If no typed error exists, add:

```go
type modelBrokerHTTPError struct { StatusCode int; Status string }
func (e modelBrokerHTTPError) Error() string { return e.Status }
```

Then update `modelBrokerRetryableError` to use `errors.As` for `modelBrokerHTTPError` and call `modelBrokerRetryableStatus`.

- [ ] **Step 3: Add retry test**

Create a test server that returns `503` once then success for Ollama `/api/chat`, set `MODEL_BROKER_RETRY_ATTEMPTS=2`, and assert generation succeeds and the server saw two calls.

- [ ] **Step 4: Run targeted tests**

Run: `go test ./internal/controlplane -run 'TestEnvModelBroker.*Retry|TestModelBrokerRetryPolicy'`

Expected: retry tests pass.

## Task 3: Improve Status Degradation

**Files:**
- Modify: `backend/internal/controlplane/model_broker_status.go`
- Test: `backend/internal/controlplane/model_executor_test.go` or `http_test.go`

- [ ] **Step 1: Add degraded status for configured but failing upstreams**

Update status handlers so:

- `not_configured` only when required base URL is empty.
- `unreachable` when network connection fails.
- `failed` when upstream responds non-2xx.
- `degraded` when the current status probe is healthy but recent ledger failures exceed a threshold in the observability layer, if this slice chooses to wire Store into status.

If Store access would make the provider too coupled, defer ledger-aware degradation and document that status remains probe-only.

- [ ] **Step 2: Test unreachable vs failed**

Add tests for an Ollama status server returning `503` and an invalid base URL.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/controlplane -run 'Test.*ModelBroker.*Status'`

Expected: status classification tests pass.

## Task 4: Safer Async Recovery

**Files:**
- Modify: `backend/internal/controlplane/llama_line_compat_tracker.go`
- Test: `backend/internal/controlplane/http_test.go`

- [ ] **Step 1: Add recovery age config**

Add:

```go
func modelBrokerRecoveryStaleAfter() time.Duration {
    if raw := strings.TrimSpace(os.Getenv("MODEL_BROKER_RECOVERY_STALE_AFTER")); raw != "" {
        if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
            return parsed
        }
    }
    return 15 * time.Minute
}
```

- [ ] **Step 2: Only fail stale running rows older than threshold**

In `recoverQueuedAsync`, change the running-row loop to:

```go
staleAfter := modelBrokerRecoveryStaleAfter()
now := time.Now().UTC()
for _, req := range running {
    if now.Sub(req.UpdatedAt) < staleAfter {
        continue
    }
    _, _ = t.store.FinishModelBrokerRequest(req.ID, FinishModelBrokerRequestInput{Status: ModelBrokerRequestFailed, Error: "recovered stale running request after API restart", CorrelationIDs: req.CorrelationIDs})
}
```

- [ ] **Step 3: Add recovery test**

Add a test that creates a running row updated now, constructs `newLlamaLineCompatTracker(store)`, and verifies it remains running when `MODEL_BROKER_RECOVERY_STALE_AFTER=1h`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/controlplane -run Recovery`

Expected: recovery tests pass.

## Task 5: Document Reliability Knobs

**Files:**
- Modify: `.env.example`
- Modify: `README.md`

- [ ] **Step 1: Add env examples**

Add to `.env.example`:

```dotenv
# Model broker reliability knobs.
# MODEL_BROKER_RETRY_ATTEMPTS=1
# MODEL_BROKER_RETRY_BACKOFF=250ms
# MODEL_BROKER_RECOVERY_STALE_AFTER=15m
```

- [ ] **Step 2: Add README sentence**

Add near model-broker config:

```markdown
`MODEL_BROKER_RETRY_ATTEMPTS`, `MODEL_BROKER_RETRY_BACKOFF`, and `MODEL_BROKER_RECOVERY_STALE_AFTER` tune bounded upstream retry and async recovery behavior; keep attempts low for interactive clients.
```

- [ ] **Step 3: Full verification and commit**

Run:

```bash
gofmt -w backend/internal/controlplane/model_executor.go backend/internal/controlplane/model_broker_status.go backend/internal/controlplane/llama_line_compat_tracker.go backend/internal/controlplane/*_test.go
make verify
git add .env.example README.md backend/internal/controlplane/model_executor.go backend/internal/controlplane/model_broker_status.go backend/internal/controlplane/llama_line_compat_tracker.go backend/internal/controlplane/*_test.go
git commit -m "feat: harden model broker upstream reliability"
```

Expected: verification passes and generated binaries are cleaned before commit.
