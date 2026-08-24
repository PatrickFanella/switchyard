package controlplane

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type API struct {
	store                 Store
	runner                *Runner
	adapterProbe          AdapterProbe
	modelBroker           ModelBrokerStatusProvider
	modelRuntime          ModelRuntimeControl
	ownedModelService     *ModelService
	compatBroker          *llamaLineCompatTracker
	openCode              OpenCodeExecutorStatusProvider
	canceler              CancelDispatcher
	scheduler             *Scheduler
	workerPool            *WorkerPool
	retention             *RetentionManager
	asyncDispatch         bool
	compatibilityObserver *CompatibilityObserver
}

type controlPlaneActorContextKey struct{}

func NewAPI(store Store, runner *Runner) *API {
	service := NewModelService(store, NewEnvModelBroker(), ModelServiceOptions{})
	api := NewAPIWithModelService(store, runner, service)
	api.ownedModelService = service
	return api
}

func (a *API) Shutdown(ctx context.Context) error {
	if a.ownedModelService == nil {
		return nil
	}
	return a.ownedModelService.Close(ctx)
}

func NewAPIWithModelService(store Store, runner *Runner, service *ModelService) *API {
	return &API{store: store, runner: runner, adapterProbe: NewEnvAdapterProbe(), modelBroker: NewEnvModelBrokerStatusProvider(), modelRuntime: service, compatBroker: &llamaLineCompatTracker{maxDepth: service.queueDepth, runtime: service}, openCode: NewEnvOpenCodeExecutorStatusProvider(), canceler: NewEnvCancelDispatcher()}
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

func (a *API) SetCompatibilityObserver(observer *CompatibilityObserver) {
	a.compatibilityObserver = observer
}

func (a *API) compatibilityObserverStatus(w http.ResponseWriter, _ *http.Request) {
	if a.compatibilityObserver == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "health": a.compatibilityObserver.Status()})
}

func (a *API) Handler() http.Handler {
	return withCORS(withRequestLimits(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scoped := *a
		scoped.store = a.store.WithContext(r.Context())
		scoped.routes().ServeHTTP(w, r)
	})))
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
	if len(l.windows) >= 10000 {
		for existingKey, existing := range l.windows {
			if existing.second < second-1 {
				delete(l.windows, existingKey)
			}
		}
	}
	window := l.windows[key]
	if _, exists := l.windows[key]; !exists && len(l.windows) >= 10000 {
		return false
	}
	if window.second != second {
		window = requestWindow{second: second}
	}
	window.count++
	l.windows[key] = window
	return window.count <= l.limit
}

func requestClientIP(r *http.Request) string {
	proxy, err := ReverseProxyAuthFromEnv()
	if err == nil && proxy.Enabled && validProxyHopToken(r.Header, proxy.Token) {
		values := r.Header.Values("X-Forwarded-For")
		if len(values) == 1 && values[0] == strings.TrimSpace(values[0]) {
			if forwarded := net.ParseIP(values[0]); forwarded != nil {
				return forwarded.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func (a *API) readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.Ready(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	if service, ok := a.modelRuntime.(*ModelService); ok {
		if err := service.HealthError(ctx); err != nil {
			writeError(w, http.StatusServiceUnavailable, "model service unavailable: "+RedactForLog(err.Error()))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ready": true})
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
	proxy, proxyConfigErr := ReverseProxyAuthFromEnv()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || ((r.URL.Path == "/health" || r.URL.Path == "/healthz" || r.URL.Path == "/readyz") && isReadOnlyControlRequest(r)) {
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
		} else if proxyUser, validIdentity := proxyIdentity(r.Header); proxyConfigErr == nil && proxy.Enabled && validIdentity && validProxyHopToken(r.Header, proxy.Token) {
			role = "admin"
			actor = "proxy:" + proxyUser
		}
		if role == "" {
			if adminToken == "" && readToken == "" && !proxy.Enabled && proxyConfigErr == nil {
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

func proxyIdentity(header http.Header) (string, bool) {
	remoteUser, remoteOK := singleHeaderValue(header, "Remote-User")
	forwardedUser, forwardedOK := singleHeaderValue(header, "X-Forwarded-User")
	if !remoteOK || !forwardedOK {
		return "", false
	}
	if remoteUser != "" && forwardedUser != "" && remoteUser != forwardedUser {
		return "", false
	}
	identity := firstNonEmpty(remoteUser, forwardedUser)
	if identity == "" || len(identity) > 256 {
		return "", false
	}
	for index, character := range []byte(identity) {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || (index > 0 && strings.ContainsRune("._@+-", rune(character)))) {
			return "", false
		}
	}
	return identity, true
}

func singleHeaderValue(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) > 1 {
		return "", false
	}
	if len(values) == 0 {
		return "", true
	}
	return values[0], true
}

func validProxyHopToken(header http.Header, configured string) bool {
	candidate, single := singleHeaderValue(header, "X-Switchyard-Proxy-Token")
	return single && secureProxyTokenEqual(candidate, configured)
}

func secureProxyTokenEqual(candidate, configured string) bool {
	candidateDigest := sha256.Sum256([]byte(candidate))
	configuredDigest := sha256.Sum256([]byte(configured))
	return subtle.ConstantTimeCompare(candidateDigest[:], configuredDigest[:]) == 1
}

func requestActor(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	return contextActor(r.Context())
}

func contextActor(ctx context.Context) string {
	if actor, ok := ctx.Value(controlPlaneActorContextKey{}).(string); ok && actor != "" {
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
	if strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/hooks/") || path == "/broker/status" {
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

func (a *API) adapters(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, a.adapterProbe.Payload(ctx))
}

func (a *API) modelBrokerStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	queue, err := a.modelRuntime.QueueStatus(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model_broker": a.modelBroker.Status(ctx), "queue": queue})
}

func (a *API) openCodeExecutorStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{"opencode_executor": a.openCode.Status(ctx)})
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

func (a *API) artifactReport(w http.ResponseWriter, r *http.Request) {
	days := 30
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxArtifactReportDays {
			writeError(w, http.StatusBadRequest, "days must be between 1 and 90")
			return
		}
		days = parsed
	}
	end := time.Now().UTC()
	start := utcDay(end).AddDate(0, 0, -days+1)
	report, err := a.store.ArtifactReport(r.Context(), ArtifactReportInput{Start: start, End: end})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "artifact report unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifact_report": report})
}

func (a *API) compatibilityRetirement(w http.ResponseWriter, r *http.Request) {
	status, err := compatibilityRetirementStatus(a.store, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *API) compatibilityReport(w http.ResponseWriter, r *http.Request) {
	start, err := time.Parse(time.DateOnly, strings.TrimSpace(r.URL.Query().Get("start")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "start must be a UTC date in YYYY-MM-DD format")
		return
	}
	end, err := time.Parse(time.DateOnly, strings.TrimSpace(r.URL.Query().Get("end")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "end must be a UTC date in YYYY-MM-DD format")
		return
	}
	observations, ok := a.store.(compatibilityObservationStore)
	if !ok {
		writeError(w, http.StatusInternalServerError, "compatibility report unavailable")
		return
	}
	report, err := observations.CompatibilityReport(r.Context(), CompatibilityReportInput{Start: start, End: end, GeneratedAt: time.Now().UTC(), Identity: CompatibilityObservationIdentity{SourceRevision: strings.TrimSpace(os.Getenv("SWITCHYARD_SOURCE_REVISION")), DeploymentID: compatibilityDeploymentID()}})
	if errors.Is(err, ErrInvalid) {
		writeError(w, http.StatusBadRequest, "window must be complete, elapsed, and between 1 and 90 UTC days")
		return
	}
	if errors.Is(err, ErrCompatibilityCoverageIncomplete) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "compatibility report unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"compatibility_report": report})
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
