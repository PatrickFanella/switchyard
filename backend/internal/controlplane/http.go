package controlplane

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type API struct {
	store         Store
	runner        *Runner
	adapterProbe  AdapterProbe
	modelBroker   ModelBrokerStatusProvider
	compatBroker  *llamaLineCompatTracker
	openCode      OpenCodeExecutorStatusProvider
	canceler      CancelDispatcher
	scheduler     *Scheduler
	workerPool    *WorkerPool
	retention     *RetentionManager
	asyncDispatch bool
}

type controlPlaneActorContextKey struct{}

func NewAPI(store Store, runner *Runner) *API {
	return &API{store: store, runner: runner, adapterProbe: NewEnvAdapterProbe(), modelBroker: NewEnvModelBrokerStatusProvider(), compatBroker: newLlamaLineCompatTracker(store), openCode: NewEnvOpenCodeExecutorStatusProvider(), canceler: NewEnvCancelDispatcher()}
}

func (a *API) SetCancelDispatcher(canceler CancelDispatcher) {
	a.canceler = canceler
}

func (a *API) SetScheduler(scheduler *Scheduler) {
	a.scheduler = scheduler
}

func (a *API) SetWorkerPool(workerPool *WorkerPool) {
	a.workerPool = workerPool
}

func (a *API) SetRetentionManager(retention *RetentionManager) {
	a.retention = retention
}

func (a *API) SetAsyncDispatch(enabled bool) {
	a.asyncDispatch = enabled
}

func (a *API) Shutdown(ctx context.Context) error {
	if a.compatBroker == nil {
		return nil
	}
	return a.compatBroker.close(ctx)
}

func (a *API) Handler() http.Handler {
	return withCORS(withRequestLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scoped := *a
		scoped.store = a.store.WithContext(r.Context())
		scoped.routes().ServeHTTP(w, r)
	})))
}

func (a *API) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /readyz", a.readiness)
	mux.HandleFunc("GET /broker/status", a.llamaLineCompatStatus)
	mux.HandleFunc("POST /v1/chat/completions", a.llamaLineCompatChatCompletions)
	mux.HandleFunc("POST /v1/completions", a.llamaLineCompatCompletions)
	mux.HandleFunc("POST /api/chat", a.llamaLineCompatChat)
	mux.HandleFunc("POST /api/generate", a.llamaLineCompatGenerate)
	mux.HandleFunc("POST /api/embed", a.llamaLineCompatEmbeddings)
	mux.HandleFunc("POST /api/embeddings", a.llamaLineCompatEmbeddings)
	mux.HandleFunc("POST /v1/embeddings", a.llamaLineCompatEmbeddings)
	mux.HandleFunc("GET /api/tags", a.llamaLineCompatTags)
	mux.HandleFunc("GET /v1/models", a.llamaLineCompatModels)
	mux.HandleFunc("GET /admin/queue", a.llamaLineCompatQueue)
	mux.HandleFunc("DELETE /admin/queue", a.llamaLineCompatQueue)
	mux.HandleFunc("DELETE /admin/queue/{id}", a.llamaLineCompatQueueItem)
	mux.HandleFunc("GET /admin/inflight", a.llamaLineCompatInflight)
	mux.HandleFunc("POST /admin/inflight/cancel", a.llamaLineCompatInflightCancel)
	mux.HandleFunc("GET /admin/logs", a.llamaLineCompatLogs)
	mux.HandleFunc("GET /admin/logs/recent", a.llamaLineCompatLogs)
	mux.HandleFunc("GET /admin/audit", a.llamaLineCompatAudit)
	mux.HandleFunc("GET /admin/audit/recent", a.llamaLineCompatAudit)
	mux.HandleFunc("GET /admin/search", a.llamaLineCompatSearch)
	mux.HandleFunc("GET /admin/stats/summary", a.llamaLineCompatStatsSummary)
	mux.HandleFunc("GET /admin/feedback", a.llamaLineCompatFeedback)
	mux.HandleFunc("POST /admin/feedback", a.llamaLineCompatFeedback)
	mux.HandleFunc("GET /ui/", a.llamaLineCompatUI)
	mux.HandleFunc("GET /ui/api/status", a.llamaLineCompatStatus)
	mux.HandleFunc("GET /ui/api/history", a.llamaLineCompatHistory)
	mux.HandleFunc("GET /ui/api/logs", a.llamaLineCompatLogs)
	mux.HandleFunc("GET /ui/api/search", a.llamaLineCompatSearch)
	mux.HandleFunc("GET /ui/api/config", a.llamaLineCompatConfig)
	mux.HandleFunc("GET /ocq/sessions", a.ocqCompatSessions)
	mux.HandleFunc("GET /ocq/sessions/{id}", a.ocqCompatSession)
	mux.HandleFunc("POST /ocq/sessions/{session}/messages/{id}/abort", a.ocqCompatAbort)
	mux.HandleFunc("GET /ocq/requests", a.ocqCompatRequests)
	mux.HandleFunc("GET /ocq/requests/{id}", a.ocqCompatRequest)
	mux.HandleFunc("POST /ocq/requests/{id}/abort", a.ocqCompatAbort)
	mux.HandleFunc("GET /ocq/settings", a.ocqCompatSettings)
	mux.HandleFunc("GET /ocq/audit", a.ocqCompatAudit)
	mux.HandleFunc("GET /ocq/logs", a.ocqCompatLogs)
	mux.HandleFunc("GET /api/history", a.llamaLineCompatHistory)
	mux.HandleFunc("GET /api/history/{id}", a.llamaLineCompatHistoryItem)
	mux.HandleFunc("GET /api/requests/{id}/result", a.llamaLineCompatRequestResult)
	mux.HandleFunc("GET /api/search", a.llamaLineCompatSearch)
	mux.HandleFunc("GET /api/logs", a.llamaLineCompatLogs)
	mux.HandleFunc("GET /api/config", a.llamaLineCompatConfig)
	mux.HandleFunc("GET /metrics", a.llamaLineCompatMetrics)
	mux.HandleFunc("GET /api/adapters", a.adapters)
	mux.HandleFunc("GET /api/models", a.modelCatalog)
	mux.HandleFunc("POST /api/models/pull", a.pullModels)
	mux.HandleFunc("GET /api/observability/model-broker", a.modelBrokerObservability)
	mux.HandleFunc("GET /api/model-broker/status", a.modelBrokerStatus)
	mux.HandleFunc("GET /api/opencode-executor/status", a.openCodeExecutorStatus)
	mux.HandleFunc("GET /api/open-pilot/states", a.openPilotStates)
	mux.HandleFunc("GET /api/open-pilot/state", a.openPilotState)
	mux.HandleFunc("POST /api/open-pilot/state/pause", a.pauseOpenPilotState)
	mux.HandleFunc("POST /api/open-pilot/state/resume", a.resumeOpenPilotState)
	mux.HandleFunc("GET /api/scheduler", a.schedulerStatus)
	mux.HandleFunc("GET /api/workers", a.workerStatus)
	mux.HandleFunc("GET /api/retention", a.retentionStatus)
	mux.HandleFunc("GET /api/operator-safety", a.operatorSafety)
	mux.HandleFunc("GET /api/pipelines", a.pipelines)
	mux.HandleFunc("GET /api/agent-profiles", a.agentProfiles)
	mux.HandleFunc("GET /api/workflow-definitions", a.workflowDefinitions)
	mux.HandleFunc("GET /api/workflow-definitions/{id}", a.workflowDefinitionAction)
	mux.HandleFunc("PUT /api/workflow-definitions/{id}", a.workflowDefinitionAction)
	mux.HandleFunc("GET /api/workflow-definitions/{id}/versions", a.workflowDefinitionVersions)
	mux.HandleFunc("POST /api/workflow-definitions/{id}/activate", a.activateWorkflowDefinition)
	mux.HandleFunc("POST /api/workflow-definitions/{id}/rollback", a.rollbackWorkflowDefinition)
	mux.HandleFunc("GET /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"workflows": workflowsOrEmpty(a.store.ListWorkflows())})
	})
	mux.HandleFunc("POST /api/workflows/open-pilot/run", a.runOpenPilotWorkflow)
	mux.HandleFunc("GET /api/triggers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"triggers": triggersOrEmpty(a.store.ListTriggers())})
	})
	mux.HandleFunc("GET /api/runs", a.listRuns)
	mux.HandleFunc("POST /api/runs", a.createRun)
	mux.HandleFunc("GET /api/runs/", a.runAction)
	mux.HandleFunc("POST /api/runs/", a.runAction)
	mux.HandleFunc("POST /api/triggers/", a.triggerAction)
	mux.HandleFunc("PATCH /api/triggers/", a.triggerAction)
	return mux
}

type requestWindow struct {
	second int64
	count  int
}

type requestLimiter struct {
	mu      sync.Mutex
	windows map[string]requestWindow
	limit   int
}

func withRequestLimits(next http.Handler) http.Handler {
	maxBody := int64(1 << 20)
	if parsed, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("SWITCHYARD_MAX_REQUEST_BYTES")), 10, 64); err == nil && parsed >= 1024 && parsed <= 16<<20 {
		maxBody = parsed
	}
	rate := 50
	if parsed, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SWITCHYARD_RATE_LIMIT_PER_SECOND"))); err == nil && parsed >= 1 && parsed <= 10000 {
		rate = parsed
	}
	limiter := &requestLimiter{windows: map[string]requestWindow{}, limit: rate}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" && r.URL.Path != "/healthz" && !limiter.allow(requestClientIP(r), time.Now()) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "request rate limit exceeded")
			return
		}
		if r.Body != nil {
			if r.ContentLength > maxBody {
				writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds configured limit")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		next.ServeHTTP(w, r)
	})
}

func (l *requestLimiter) allow(key string, now time.Time) bool {
	second := now.Unix()
	l.mu.Lock()
	defer l.mu.Unlock()
	window := l.windows[key]
	if window.second != second {
		window = requestWindow{second: second}
	}
	window.count++
	l.windows[key] = window
	if len(l.windows) > 10000 {
		for existingKey, existing := range l.windows {
			if existing.second < second-1 {
				delete(l.windows, existingKey)
			}
		}
	}
	return window.count <= l.limit
}

func requestClientIP(r *http.Request) string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SWITCHYARD_REVERSE_PROXY_AUTH")), "true") {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			return forwarded
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	input := ListRunsPageInput{Limit: limit}
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		created, id, err := decodeRunCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		input.BeforeCreated = &created
		input.BeforeID = id
	}
	page, err := a.store.ListRunsPageContext(r.Context(), input)
	if err != nil {
		writeControlError(w, err)
		return
	}
	nextCursor := ""
	if page.HasMore && len(page.Runs) > 0 {
		last := page.Runs[len(page.Runs)-1]
		nextCursor = encodeRunCursor(last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runsOrEmpty(page.Runs), "next_cursor": nextCursor})
}

func (a *API) readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.Ready(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ready": true})
}

func encodeRunCursor(created time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(created.UTC().Format(time.RFC3339Nano) + "\n" + id))
}

func decodeRunCursor(cursor string) (time.Time, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", err
	}
	createdText, id, ok := strings.Cut(string(decoded), "\n")
	if !ok || strings.TrimSpace(id) == "" {
		return time.Time{}, "", fmt.Errorf("malformed cursor")
	}
	created, err := time.Parse(time.RFC3339Nano, createdText)
	return created, id, err
}

// ControlPlaneHandler serves the operator API without exposing model-provider
// compatible inference routes. Authentication and authorization are layered on
// this handler so deployments can keep it on a separate trust boundary.
func (a *API) ControlPlaneHandler() http.Handler {
	return routeBoundary(controlPlaneAuth(a.Handler()), controlPlaneRouteAllowed)
}

// ModelIngressHandler exposes only the OpenAI/Ollama-compatible inference and
// status contract. It deliberately excludes run, trigger, workflow, history,
// audit, and administrative mutation routes.
func (a *API) ModelIngressHandler() http.Handler {
	return routeBoundary(a.Handler(), modelIngressRouteAllowed)
}

func routeBoundary(next http.Handler, allowed func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r) {
			writeError(w, http.StatusNotFound, "route not available on this listener")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func controlPlaneAuth(next http.Handler) http.Handler {
	adminToken := strings.TrimSpace(os.Getenv("SWITCHYARD_CONTROL_API_TOKEN"))
	readToken := strings.TrimSpace(os.Getenv("SWITCHYARD_CONTROL_READ_TOKEN"))
	trustProxy := strings.EqualFold(strings.TrimSpace(os.Getenv("SWITCHYARD_REVERSE_PROXY_AUTH")), "true")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || ((r.URL.Path == "/health" || r.URL.Path == "/healthz" || r.URL.Path == "/readyz") && r.Method == http.MethodGet) {
			next.ServeHTTP(w, r)
			return
		}
		role := ""
		actor := ""
		token := bearerToken(r.Header.Get("Authorization"))
		if secureTokenEqual(token, adminToken) {
			role = "admin"
			actor = "token:admin"
		} else if secureTokenEqual(token, readToken) {
			role = "read"
			actor = "token:read"
		} else if proxyUser := firstNonEmpty(strings.TrimSpace(r.Header.Get("X-Forwarded-User")), strings.TrimSpace(r.Header.Get("Remote-User"))); trustProxy && proxyUser != "" {
			role = "admin"
			actor = "proxy:" + proxyUser
		}
		if role == "" {
			if adminToken == "" && readToken == "" && !trustProxy {
				writeError(w, http.StatusServiceUnavailable, "control-plane authentication is not configured")
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="switchyard-control-plane"`)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if role != "admin" && !isReadOnlyControlRequest(r) {
			writeError(w, http.StatusForbidden, "admin authorization required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), controlPlaneActorContextKey{}, actor)))
	})
}

func requestActor(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	if actor, ok := r.Context().Value(controlPlaneActorContextKey{}).(string); ok && actor != "" {
		return actor
	}
	return "internal"
}

func secureTokenEqual(candidate, configured string) bool {
	if candidate == "" || configured == "" || len(candidate) != len(configured) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(configured)) == 1
}

func isReadOnlyControlRequest(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

func controlPlaneRouteAllowed(r *http.Request) bool {
	path := r.URL.Path
	if strings.HasPrefix(path, "/v1/") || path == "/broker/status" {
		return false
	}
	switch path {
	case "/api/chat", "/api/generate", "/api/embed", "/api/embeddings", "/api/tags":
		return false
	default:
		return true
	}
}

func modelIngressRouteAllowed(r *http.Request) bool {
	path := r.URL.Path
	if path == "/health" || path == "/healthz" || path == "/readyz" || path == "/metrics" || path == "/broker/status" {
		return r.Method == http.MethodGet
	}
	if strings.HasPrefix(path, "/v1/") {
		switch path {
		case "/v1/chat/completions", "/v1/completions", "/v1/embeddings":
			return r.Method == http.MethodPost
		case "/v1/models":
			return r.Method == http.MethodGet
		}
	}
	switch path {
	case "/api/chat", "/api/generate", "/api/embed", "/api/embeddings":
		return r.Method == http.MethodPost
	case "/api/tags":
		return r.Method == http.MethodGet
	default:
		return false
	}
}

func (a *API) operatorSafety(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, NewEnvOperatorSafetyStatus())
}

func (a *API) pipelines(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"pipelines": pipelinesOrEmpty(a.runner.Pipelines())})
}

func (a *API) agentProfiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"profiles": agentProfilesOrEmpty(a.runner.AgentProfiles())})
}

func (a *API) workflowDefinitions(w http.ResponseWriter, r *http.Request) {
	definitions, err := a.store.ListWorkflowDefinitions()
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definitions": workflowDefinitionsOrEmpty(definitions)})
}

func (a *API) workflowDefinitionAction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusNotFound, "workflow definition not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.getWorkflowDefinition(w, r, id)
	case http.MethodPut:
		a.updateWorkflowDefinition(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "workflow definition not found")
	}
}

func (a *API) getWorkflowDefinition(w http.ResponseWriter, r *http.Request, id string) {
	definition, err := a.store.WorkflowDefinition(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": definition})
}

func (a *API) workflowDefinitionVersions(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	versions, err := a.store.ListWorkflowDefinitionVersions(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definitions": versions})
}

func (a *API) activateWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	definition, err := a.store.WorkflowDefinition(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := validateWorkflowDefinitionForActivation(definition); err != nil {
		writeControlError(w, err)
		return
	}
	activated, err := a.store.ActivateWorkflowDefinition(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := a.store.UpsertWorkflow(workflowFromDefinition(activated)); err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": activated})
}

func (a *API) rollbackWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var input WorkflowDefinitionRollbackInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	versions, err := a.store.ListWorkflowDefinitionVersions(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	var target *WorkflowDefinition
	for index := range versions {
		if versions[index].Version == input.Version {
			target = &versions[index]
			break
		}
	}
	if target == nil {
		writeControlError(w, ErrNotFound)
		return
	}
	if err := validateWorkflowDefinitionForActivation(*target); err != nil {
		writeControlError(w, err)
		return
	}
	rolledBack, err := a.store.RollbackWorkflowDefinition(id, input.Version)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := a.store.UpsertWorkflow(workflowFromDefinition(rolledBack)); err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": rolledBack, "rolled_back_from_version": input.Version})
}

func workflowFromDefinition(definition WorkflowDefinition) Workflow {
	return Workflow{ID: definition.ID, Kind: WorkflowKind(definition.ID), Name: definition.Name, Description: definition.Description, Owns: append([]string(nil), definition.Owns...), Uses: append([]string(nil), definition.Uses...), Status: definition.Status}
}

func (a *API) updateWorkflowDefinition(w http.ResponseWriter, r *http.Request, id string) {
	var definition WorkflowDefinition
	if err := json.NewDecoder(r.Body).Decode(&definition); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	definition.ID = id
	definition.Pipeline.WorkflowID = id
	if err := validateWorkflowDefinitionWrite(definition); err != nil {
		writeControlError(w, err)
		return
	}
	definition.Status = "draft"
	saved, err := a.store.UpsertWorkflowDefinition(definition)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": saved})
}

func validateWorkflowDefinitionWrite(definition WorkflowDefinition) error {
	if definition.Status != "" && !strings.EqualFold(definition.Status, "draft") {
		return ErrInvalid
	}
	return validateWorkflowDefinitionStructure(definition, false)
}

func validateWorkflowDefinitionForActivation(definition WorkflowDefinition) error {
	return validateWorkflowDefinitionStructure(definition, true)
}

func validateWorkflowDefinitionStructure(definition WorkflowDefinition, requireExecutable bool) error {
	seenIDs := map[string]bool{}
	seenPositions := map[int]bool{}
	for _, step := range definition.Pipeline.Steps {
		id := strings.TrimSpace(step.ID)
		if id == "" || seenIDs[id] {
			return ErrInvalid
		}
		seenIDs[id] = true
		if step.Position < 1 || seenPositions[step.Position] {
			return ErrInvalid
		}
		seenPositions[step.Position] = true
		switch strings.TrimSpace(step.Executor) {
		case "internal":
		case "model":
			if requireExecutable && strings.TrimSpace(firstNonEmpty(step.Inputs["prompt"], step.Inputs["template"])) == "" {
				return ErrInvalid
			}
		case "command":
			if requireExecutable && strings.TrimSpace(step.Inputs["command"]) == "" {
				return ErrInvalid
			}
		case "http":
			if requireExecutable && (strings.TrimSpace(step.Inputs["url"]) == "" || !strings.EqualFold(strings.TrimSpace(step.Inputs["allow_http"]), "true")) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	for position := 1; position <= len(definition.Pipeline.Steps); position++ {
		if !seenPositions[position] {
			return ErrInvalid
		}
	}
	if definition.InputShape.Type != "" && definition.InputShape.Type != "object" {
		return ErrInvalid
	}
	for _, required := range definition.InputShape.Required {
		if _, ok := definition.InputShape.Properties[required]; !ok {
			return ErrInvalid
		}
	}
	for _, property := range definition.InputShape.Properties {
		switch property.Type {
		case "string", "number", "integer", "boolean", "array", "object":
		default:
			return ErrInvalid
		}
	}
	return nil
}

func (a *API) adapters(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, a.adapterProbe.Payload(ctx))
}

func (a *API) modelBrokerStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{"model_broker": a.modelBroker.Status(ctx)})
}

func (a *API) openCodeExecutorStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{"opencode_executor": a.openCode.Status(ctx)})
}

func (a *API) openPilotStates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"states": openPilotStatesOrEmpty(a.store.ListOpenPilotIssueStates())})
}

func (a *API) openPilotState(w http.ResponseWriter, r *http.Request) {
	repo, issue, ok := openPilotRepoIssueFromQuery(w, r)
	if !ok {
		return
	}
	state, err := a.store.OpenPilotIssueState(repo, issue)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, openPilotStateResponse{State: state, NextAction: openPilotNextAction(state)})
}

func (a *API) pauseOpenPilotState(w http.ResponseWriter, r *http.Request) {
	repo, issue, ok := openPilotRepoIssueFromQuery(w, r)
	if !ok {
		return
	}
	state, err := a.store.OpenPilotIssueState(repo, issue)
	if err != nil {
		writeControlError(w, err)
		return
	}
	state.ResumeInputs = mergeOpenPilotResumeStateInputs(state)
	state.Phase = OpenPilotPhasePaused
	state.LastError = firstNonEmpty(state.LastError, "paused by operator")
	state, err = a.store.UpsertOpenPilotIssueState(state)
	if err != nil {
		writeControlError(w, err)
		return
	}
	a.cancelOpenPilotRuns(repo, issue, "Paused Open Pilot issue "+repo+"#"+issue)
	writeJSON(w, http.StatusOK, openPilotStateResponse{State: state, NextAction: openPilotNextAction(state)})
}

func (a *API) resumeOpenPilotState(w http.ResponseWriter, r *http.Request) {
	repo, issue, ok := openPilotRepoIssueFromQuery(w, r)
	if !ok {
		return
	}
	state, err := a.store.OpenPilotIssueState(repo, issue)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if !isOpenPilotResumablePhase(state.Phase) {
		writeError(w, http.StatusBadRequest, "state is not resumable")
		return
	}
	originalPhase := state.Phase
	if openPilotHasActiveRun(a.store, repo, issue) {
		writeError(w, http.StatusConflict, "open-pilot issue already has an active run")
		return
	}
	inputs := mergeOpenPilotResumeStateInputs(state)
	if inputs == nil {
		inputs = map[string]string{}
	}
	inputs["repo"] = repo
	inputs["issue"] = issue
	state, err = a.store.ClaimOpenPilotIssueStateForResume(repo, issue, inputs)
	if err != nil {
		writeControlError(w, err)
		return
	}
	run, err := a.dispatchRun(r.Context(), CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: inputs})
	if err != nil {
		state.Phase = OpenPilotPhaseFailed
		if originalPhase == OpenPilotPhasePaused {
			state.Phase = OpenPilotPhasePaused
		}
		state.LastError = err.Error()
		state.FailingGate = "resume"
		state.OperatorSummary = "Resume dispatch failed: " + err.Error()
		state.RecommendedNextAction = "resume"
		_, _ = a.store.UpsertOpenPilotIssueState(state)
		writeControlError(w, err)
		return
	}
	if fresh, err := a.store.OpenPilotIssueState(repo, issue); err == nil {
		state = fresh
	}
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, openPilotStateResponse{State: state, Run: &run, NextAction: openPilotNextAction(state)})
}

func openPilotHasActiveRun(store Store, repo, issue string) bool {
	for _, run := range store.ListRuns() {
		if run.WorkflowID == string(WorkflowOpenPilot) && run.Inputs["repo"] == repo && run.Inputs["issue"] == issue && (run.Status == RunQueued || run.Status == RunRunning) {
			return true
		}
	}
	return false
}

func (a *API) cancelOpenPilotRuns(repo, issue, message string) {
	for _, run := range a.store.ListRuns() {
		if run.WorkflowID != string(WorkflowOpenPilot) || run.Inputs["repo"] != repo || run.Inputs["issue"] != issue {
			continue
		}
		if run.Status != RunQueued && run.Status != RunRunning {
			continue
		}
		_, _ = a.store.AddEvent(run.ID, "open-pilot.pause", message)
		if a.canceler != nil {
			if detail, err := a.store.RunDetail(run.ID); err == nil {
				for _, event := range cancelRunDetail(context.Background(), a.canceler, detail).Events {
					_, _ = a.store.AddEvent(run.ID, event.Type, event.Message)
				}
			}
		}
		_, _ = a.store.FinishRun(run.ID, RunCanceled, message)
	}
}

func (a *API) schedulerStatus(w http.ResponseWriter, r *http.Request) {
	if a.scheduler == nil {
		writeJSON(w, http.StatusOK, SchedulerStatus{Enabled: false, Running: false})
		return
	}
	writeJSON(w, http.StatusOK, a.scheduler.Status())
}

func (a *API) workerStatus(w http.ResponseWriter, r *http.Request) {
	if a.workerPool == nil {
		writeJSON(w, http.StatusOK, WorkerPoolStatus{Enabled: false, Running: false})
		return
	}
	writeJSON(w, http.StatusOK, a.workerPool.Status())
}

func (a *API) retentionStatus(w http.ResponseWriter, r *http.Request) {
	if a.retention == nil {
		writeJSON(w, http.StatusOK, map[string]any{"retention": RetentionStatus{Running: false}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"retention": a.retention.Status()})
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	var input CreateRunInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	run, err := a.dispatchRun(r.Context(), input)
	if err != nil {
		writeControlError(w, err)
		return
	}
	_, _ = a.store.AddEvent(run.ID, "audit.run.created", "Run created by "+requestActor(r))
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]Run{"run": run})
}

func (a *API) runOpenPilotWorkflow(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Repo  string `json:"repo"`
		Issue string `json:"issue"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if input.Repo == "" || input.Issue == "" {
		writeError(w, http.StatusBadRequest, "repo and issue are required")
		return
	}
	run, err := a.dispatchRun(r.Context(), CreateRunInput{WorkflowID: string(WorkflowOpenPilot), TriggerID: "manual-open-pilot", Inputs: map[string]string{"repo": input.Repo, "issue": input.Issue}})
	if err != nil {
		writeControlError(w, err)
		return
	}
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]Run{"run": run})
}

func (a *API) dispatchRun(ctx context.Context, input CreateRunInput) (Run, error) {
	if a.asyncDispatch {
		return a.runner.EnqueueContext(ctx, input)
	}
	return a.runner.RunOnce(ctx, input)
}

func (a *API) runAction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	if id == "" {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if actionRunID, action, ok := strings.Cut(id, "/"); ok {
		if action == "steps" || strings.HasPrefix(action, "steps/") {
			a.stepAction(w, r, actionRunID, strings.TrimPrefix(action, "steps"))
			return
		}
		switch {
		case r.Method == http.MethodPost && action == "retry":
			a.retryRun(w, r, actionRunID)
			return
		case r.Method == http.MethodPost && action == "cancel":
			a.cancelRun(w, r, actionRunID)
			return
		default:
			writeError(w, http.StatusNotFound, "run action not found")
			return
		}
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusNotFound, "run action not found")
		return
	}
	detail, err := a.store.RunDetailContext(r.Context(), id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	detail.Events = eventsOrEmpty(detail.Events)
	detail.Artifacts = artifactsOrEmpty(detail.Artifacts)
	detail.Steps = stepsOrEmpty(detail.Steps)
	truncated := map[string]bool{}
	if len(detail.Events) > 500 {
		detail.Events = detail.Events[len(detail.Events)-500:]
		truncated["events"] = true
	}
	if len(detail.Artifacts) > 200 {
		detail.Artifacts = detail.Artifacts[len(detail.Artifacts)-200:]
		truncated["artifacts"] = true
	}
	if len(detail.Steps) > 200 {
		detail.Steps = detail.Steps[len(detail.Steps)-200:]
		truncated["steps"] = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": detail.Run, "steps": detail.Steps, "events": detail.Events, "artifacts": detail.Artifacts, "truncated": truncated})
}

func (a *API) stepAction(w http.ResponseWriter, r *http.Request, runID string, path string) {
	if path == "" || path == "/" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusNotFound, "step action not found")
			return
		}
		steps := stepsOrEmpty(a.store.ListRunSteps(runID))
		if len(steps) > 200 {
			steps = steps[len(steps)-200:]
		}
		writeJSON(w, http.StatusOK, map[string][]RunStep{"steps": steps})
		return
	}
	path = strings.TrimPrefix(path, "/")
	stepID, action, ok := strings.Cut(path, "/")
	if !ok || stepID == "" || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "step action not found")
		return
	}
	switch action {
	case "retry":
		step, err := a.store.RunStep(stepID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		if step.RunID != runID {
			writeError(w, http.StatusNotFound, "step not found for run")
			return
		}
		detail, err := a.store.RunDetailContext(r.Context(), runID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		inputs := retryRunInputsFromStep(detail, step)
		applyOpenPilotRetryInputsFromState(a.store, detail, step, inputs)
		run, err := a.dispatchRun(r.Context(), CreateRunInput{WorkflowID: detail.Run.WorkflowID, TriggerID: detail.Run.TriggerID, Inputs: inputs, PipelineSnapshot: detail.Run.PipelineSnapshot})
		if err != nil {
			writeControlError(w, err)
			return
		}
		_, _ = a.store.AddEvent(run.ID, "run.step.retry", "Retried "+step.Name+" from "+runID+" by "+requestActor(r))
		status := http.StatusCreated
		if a.asyncDispatch {
			w.Header().Set("Location", "/api/runs/"+run.ID)
			status = http.StatusAccepted
		}
		writeJSON(w, status, map[string]any{"run": run, "step": step})
	case "cancel":
		step, err := a.store.RunStep(stepID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		if step.RunID != runID {
			writeError(w, http.StatusNotFound, "step not found for run")
			return
		}
		step, err = a.store.CancelRunStep(stepID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		a.runner.CancelRun(runID)
		writeJSON(w, http.StatusOK, map[string]RunStep{"step": step})
	default:
		writeError(w, http.StatusNotFound, "step action not found")
	}
}

func retryRunInputsFromStep(detail RunDetail, step RunStep) map[string]string {
	inputs := cloneMap(detail.Run.Inputs)
	if inputs == nil {
		inputs = map[string]string{}
	}
	inputs["retry_from_run"] = detail.Run.ID
	inputs["retry_from_step_id"] = step.ID
	inputs["retry_from_step"] = step.Name
	inputs["retry_from_step_position"] = strconv.Itoa(step.Position)
	for _, existing := range detail.Steps {
		prefix := "resume." + existing.Name + "."
		for key, value := range existing.Inputs {
			inputs[prefix+key] = value
		}
		if existing.OutputSummary != "" {
			inputs[prefix+"summary"] = existing.OutputSummary
		}
		for key, value := range existing.CorrelationIDs {
			inputs[prefix+"correlation."+key] = value
		}
	}
	return inputs
}

func applyOpenPilotRetryInputsFromState(store Store, detail RunDetail, step RunStep, inputs map[string]string) {
	if store == nil || detail.Run.WorkflowID != string(WorkflowOpenPilot) || step.Name != "open-pilot.ai-review" || inputs == nil {
		return
	}
	repo := detail.Run.Inputs["repo"]
	issue := detail.Run.Inputs["issue"]
	if repo == "" || issue == "" {
		return
	}
	state, err := store.OpenPilotIssueState(repo, issue)
	if err != nil {
		return
	}
	for key, value := range state.ResumeInputs {
		if value != "" {
			inputs[key] = value
		}
	}
	if retryStep := state.ResumeInputs["retry_from_step"]; retryStep != "" {
		inputs["retry_from_step"] = retryStep
	}
	if retryPosition := state.ResumeInputs["retry_from_step_position"]; retryPosition != "" {
		inputs["retry_from_step_position"] = retryPosition
	}
}

func (a *API) retryRun(w http.ResponseWriter, r *http.Request, id string) {
	detail, err := a.store.RunDetailContext(r.Context(), id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if detail.Run.WorkflowID == string(WorkflowOpenPilot) {
		if repo, issue := detail.Run.Inputs["repo"], detail.Run.Inputs["issue"]; repo != "" && issue != "" {
			if state, stateErr := a.store.OpenPilotIssueState(repo, issue); stateErr == nil && isOpenPilotResumablePhase(state.Phase) {
				writeError(w, http.StatusConflict, "use the Open Pilot lifecycle resume action for this issue")
				return
			}
		}
	}
	run, err := a.dispatchRun(r.Context(), CreateRunInput{WorkflowID: detail.Run.WorkflowID, TriggerID: detail.Run.TriggerID, Inputs: detail.Run.Inputs, PipelineSnapshot: detail.Run.PipelineSnapshot})
	if err != nil {
		writeControlError(w, err)
		return
	}
	_, _ = a.store.AddEvent(run.ID, "run.retry", "Retried from "+id+" by "+requestActor(r))
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]Run{"run": run})
}

func (a *API) cancelRun(w http.ResponseWriter, r *http.Request, id string) {
	detail, err := a.store.RunDetailContext(r.Context(), id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if detail.Run.Status != RunQueued && detail.Run.Status != RunRunning {
		writeError(w, http.StatusBadRequest, "only queued or running runs can be canceled")
		return
	}
	_, _ = a.store.AddEvent(id, "run.cancel.requested", "Cancellation requested by "+requestActor(r))
	a.runner.CancelRun(id)
	if a.canceler != nil {
		for _, event := range cancelRunDetail(r.Context(), a.canceler, detail).Events {
			_, _ = a.store.AddEvent(id, event.Type, event.Message)
		}
	}
	run, err := a.store.FinishRun(id, RunCanceled, "Canceled by operator")
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]Run{"run": run})
}

func workflowsOrEmpty(items []Workflow) []Workflow {
	if items == nil {
		return []Workflow{}
	}
	return items
}

func workflowDefinitionsOrEmpty(items []WorkflowDefinition) []WorkflowDefinition {
	if items == nil {
		return []WorkflowDefinition{}
	}
	return items
}

func pipelinesOrEmpty(items []Pipeline) []Pipeline {
	if items == nil {
		return []Pipeline{}
	}
	return items
}

func agentProfilesOrEmpty(items []AgentProfile) []AgentProfile {
	if items == nil {
		return []AgentProfile{}
	}
	return items
}

func triggersOrEmpty(items []Trigger) []Trigger {
	if items == nil {
		return []Trigger{}
	}
	return items
}

func runsOrEmpty(items []Run) []Run {
	if items == nil {
		return []Run{}
	}
	return items
}

func eventsOrEmpty(items []RunEvent) []RunEvent {
	if items == nil {
		return []RunEvent{}
	}
	return items
}

func artifactsOrEmpty(items []Artifact) []Artifact {
	if items == nil {
		return []Artifact{}
	}
	return items
}

func stepsOrEmpty(items []RunStep) []RunStep {
	if items == nil {
		return []RunStep{}
	}
	return items
}

func staticAdapterPayload() AdapterPayload {
	return AdapterPayload{
		Queues:   []AdapterQueue{},
		Sessions: []AdapterSession{},
		Logs:     []AdapterLog{},
		Prompts:  []AdapterPrompt{},
		Config: AdapterConfig{
			Environment: firstNonEmpty(strings.TrimSpace(os.Getenv("SWITCHYARD_ENVIRONMENT")), "unknown"),
			Version:     firstNonEmpty(strings.TrimSpace(os.Getenv("SWITCHYARD_VERSION")), "development"),
			BaseURL:     "/api",
			Features:    []string{"queues", "sessions", "logs", "prompts", "config", "agents", "skills", "health"},
			Limits:      map[string]int{},
		},
		Agents: []AdapterAgent{},
		Skills: []AdapterSkill{},
		Health: AdapterHealth{
			Status: "healthy",
			Services: []AdapterService{{
				Name:    "api",
				Status:  "healthy",
				Details: "control-plane handler responding",
			}},
			CheckedAt: time.Now().UTC(),
		},
		LlamaLine: AdapterLlamaLine{
			Requests:  []AdapterLlamaRequest{},
			Stats:     AdapterLlamaStats{},
			Search:    []AdapterLlamaSearchResult{},
			Upstreams: []AdapterLlamaUpstream{},
		},
		OCQ: AdapterOCQ{
			Requests: []AdapterOCQRequest{},
			Logs:     []AdapterOCQLog{},
			Sessions: []AdapterOCQSession{},
			Settings: AdapterOCQSettings{
				Provider:    "unconfigured",
				CORSAllowed: []string{},
			},
		},
		Blacktower: AdapterBlacktower{
			Tasks:   []AdapterBlacktowerTask{},
			Reports: []AdapterBlacktowerReport{},
			SchedulerHealth: AdapterSchedulerHealth{
				Status: "unconfigured",
			},
		},
		Changemaker: AdapterChangemaker{
			Posts: []AdapterChangemakerPost{},
			Service: AdapterChangemakerService{
				Name:   "changemaker",
				Status: "unconfigured",
			},
			RunControl: AdapterChangemakerRunControl{
				Mode: "disabled",
			},
		},
	}
}

func (a *API) triggerAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/triggers/")
	triggerID, action, hasAction := strings.Cut(path, "/")
	if triggerID == "" {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}
	if r.Method == http.MethodPatch && !hasAction {
		a.updateTrigger(w, r, triggerID)
		return
	}
	if r.Method != http.MethodPost || !hasAction || action != "run" {
		writeError(w, http.StatusNotFound, "trigger action not found")
		return
	}
	var run Run
	var err error
	if a.asyncDispatch {
		run, err = a.runner.EnqueueTrigger(triggerID)
	} else {
		run, err = a.runner.RunTrigger(r.Context(), triggerID)
	}
	if err != nil {
		writeControlError(w, err)
		return
	}
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]Run{"run": run})
}

func (a *API) updateTrigger(w http.ResponseWriter, r *http.Request, triggerID string) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if input.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	trigger, err := a.store.SetTriggerEnabled(triggerID, *input.Enabled)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]Trigger{"trigger": trigger})
}

func writeControlError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]map[string]string{"error": {"message": message}})
}

func writeJSONDecodeError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds configured limit")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid JSON body")
}

type openPilotStateResponse struct {
	State      OpenPilotIssueState `json:"state"`
	Run        *Run                `json:"run,omitempty"`
	NextAction string              `json:"next_action"`
}

func openPilotRepoIssueFromQuery(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	issue := strings.TrimSpace(r.URL.Query().Get("issue"))
	if repo == "" || issue == "" {
		writeError(w, http.StatusBadRequest, "repo and issue are required")
		return "", "", false
	}
	return repo, issue, true
}

func openPilotStatesOrEmpty(items []OpenPilotIssueState) []OpenPilotIssueState {
	if items == nil {
		return []OpenPilotIssueState{}
	}
	return items
}

func mergeOpenPilotResumeStateInputs(state OpenPilotIssueState) map[string]string {
	inputs := cloneMap(state.ResumeInputs)
	if inputs == nil {
		inputs = map[string]string{}
	}
	inputs["repo"] = state.Repo
	inputs["issue"] = state.Issue
	if state.Branch != "" {
		inputs["resume.open-pilot.branch-plan.branch"] = state.Branch
		inputs["resume.open-pilot.branch-plan.correlation.branch"] = state.Branch
	}
	if state.Base != "" {
		inputs["resume.open-pilot.branch-plan.base_branch"] = state.Base
	}
	if state.WorktreePath != "" {
		inputs["resume.open-pilot.branch-plan.worktree_path"] = state.WorktreePath
		inputs["resume.open-pilot.branch-plan.correlation.worktree_path"] = state.WorktreePath
	}
	if state.PullRequestURL != "" {
		inputs["resume.open-pilot.pr-plan.correlation.pull_request"] = state.PullRequestURL
	}
	if state.PullRequestNum > 0 {
		inputs["resume.open-pilot.pr-plan.pull_request_number"] = strconv.Itoa(state.PullRequestNum)
	}
	if state.MergeCommitSHA != "" {
		inputs["resume.open-pilot.merge-pr.merge_commit_sha"] = state.MergeCommitSHA
	}
	if state.IssueClosed {
		inputs["resume.open-pilot.close-issue.issue_closed"] = "true"
	}
	step, position := openPilotResumeCursorForState(state)
	if step != "" && position != "" {
		if state.Phase == OpenPilotPhaseFailed || state.Phase == OpenPilotPhasePaused {
			inputs["retry_from_step"] = step
			inputs["retry_from_step_position"] = position
		} else {
			inputs["retry_from_step"] = step
			inputs["retry_from_step_position"] = position
		}
	}
	return inputs
}

func openPilotResumeCursorForState(state OpenPilotIssueState) (string, string) {
	if (state.Phase == OpenPilotPhaseFailed || state.Phase == OpenPilotPhasePaused) && state.ResumeInputs != nil {
		if state.LastStepName != "" && !openPilotShouldUseStoredRepairCursor(state) {
			if pos := openPilotStepPosition(state.LastStepName); pos != "" {
				return state.LastStepName, pos
			}
		}
		if step, pos := state.ResumeInputs["retry_from_step"], state.ResumeInputs["retry_from_step_position"]; step != "" && pos != "" {
			if resolved := openPilotStepPosition(step); resolved != "" {
				return step, resolved
			}
			return step, pos
		}
		if state.LastStepName != "" {
			if pos := openPilotStepPosition(state.LastStepName); pos != "" {
				return state.LastStepName, pos
			}
		}
	}
	switch state.Phase {
	case OpenPilotPhaseRequested:
		return "open-pilot.validate-issue", "1"
	case OpenPilotPhaseValidated:
		return "open-pilot.branch-plan", "2"
	case OpenPilotPhaseBranchPrepared:
		return "open-pilot.prepare-prompt", "3"
	case OpenPilotPhaseAgentCompleted:
		if state.ResumeInputs != nil && strings.TrimSpace(state.ResumeInputs["test_command"]) != "" {
			return "open-pilot.verify-command", "5"
		}
		return "open-pilot.commit-changes", "6"
	case OpenPilotPhaseVerified:
		return "open-pilot.commit-changes", "6"
	case OpenPilotPhaseCommitted:
		return "open-pilot.push-branch", "7"
	case OpenPilotPhasePushed:
		return "open-pilot.pr-plan", "8"
	case OpenPilotPhasePRCreated:
		return "open-pilot.ai-review", "9"
	case OpenPilotPhaseAIReviewed:
		return "open-pilot.ci-checks", "10"
	case OpenPilotPhaseCIPassed, OpenPilotPhaseMergeReady:
		return "open-pilot.merge-pr", "11"
	case OpenPilotPhaseMerged:
		return "open-pilot.close-issue", "12"
	case OpenPilotPhaseFailed, OpenPilotPhasePaused:
		return "open-pilot.agent-work", "4"
	default:
		return "", ""
	}
}

func openPilotShouldUseStoredRepairCursor(state OpenPilotIssueState) bool {
	if state.FailingGate == "ai_review" || state.GateStatus["ai_review"] == "changes_requested" {
		return true
	}
	return state.ResumeInputs != nil && strings.TrimSpace(state.ResumeInputs["ai_review.feedback"]) != ""
}

func openPilotStepPosition(name string) string {
	switch name {
	case "open-pilot.validate-issue":
		return "1"
	case "open-pilot.branch-plan":
		return "2"
	case "open-pilot.prepare-prompt":
		return "3"
	case "open-pilot.agent-work":
		return "4"
	case "open-pilot.verify-command":
		return "5"
	case "open-pilot.commit-changes":
		return "6"
	case "open-pilot.push-branch":
		return "7"
	case "open-pilot.pr-plan":
		return "8"
	case "open-pilot.ai-review":
		return "9"
	case "open-pilot.ci-checks":
		return "10"
	case "open-pilot.merge-pr":
		return "11"
	case "open-pilot.close-issue":
		return "12"
	default:
		return ""
	}
}

func openPilotNextAction(state OpenPilotIssueState) string {
	switch state.Phase {
	case OpenPilotPhaseFailed, OpenPilotPhasePaused:
		return "Resume"
	case OpenPilotPhaseIssueClosed:
		return "Complete"
	case OpenPilotPhaseMerged:
		if state.IssueClosed {
			return "Complete"
		}
		return "Close issue"
	case OpenPilotPhaseMergeReady, OpenPilotPhaseCIPassed:
		return "Merge PR"
	case OpenPilotPhaseAIReviewed:
		if state.GateStatus["ci_checks"] == "passed" {
			return "Merge PR"
		}
		return "Wait for CI"
	case OpenPilotPhasePRCreated:
		if state.GateStatus["ai_review"] == "approved" {
			if state.GateStatus["ci_checks"] == "passed" {
				return "Merge PR"
			}
			return "Wait for CI"
		}
		return "AI review"
	case OpenPilotPhasePushed:
		return "Create PR"
	case OpenPilotPhaseVerified:
		return "Push branch"
	case OpenPilotPhaseAgentCompleted:
		return "Verify"
	case OpenPilotPhaseBranchPrepared:
		return "Run agent"
	case OpenPilotPhaseValidated:
		return "Prepare branch"
	case OpenPilotPhaseRequested:
		return "Validate issue"
	default:
		return "Continue"
	}
}

func withCORS(next http.Handler) http.Handler {
	allowedOrigins := map[string]struct{}{}
	for _, origin := range strings.Split(os.Getenv("SWITCHYARD_CORS_ALLOWED_ORIGINS"), ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowedOrigins[origin] = struct{}{}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if _, ok := allowedOrigins[origin]; ok && origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "content-type, authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			if origin != "" {
				if _, ok := allowedOrigins[origin]; !ok {
					writeError(w, http.StatusForbidden, "origin not allowed")
					return
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
