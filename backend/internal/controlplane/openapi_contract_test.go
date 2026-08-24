package controlplane

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPIPathsMatchRegisteredRoutes(t *testing.T) {
	documentBytes, err := os.ReadFile("../../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(documentBytes, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}
	store := NewStore(nil)
	mux := NewAPI(store, NewRunner(store)).routes()
	methods := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}
	for path, operations := range document.Paths {
		for method := range operations {
			if !methods[strings.ToLower(method)] {
				continue
			}
			requestPath := contractRequestPath(path)
			request, err := http.NewRequest(strings.ToUpper(method), "http://switchyard"+requestPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, pattern := mux.Handler(request)
			if pattern == "" {
				t.Errorf("OpenAPI operation %s %s has no registered route", strings.ToUpper(method), path)
			}
		}
	}

	httpFiles, err := filepath.Glob("http*.go")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for path, operations := range document.Paths {
		for method := range operations {
			if methods[strings.ToLower(method)] {
				documented[routeContractKey(strings.ToUpper(method), path)] = true
			}
		}
	}
	registered := map[string]bool{}
	for _, filename := range httpFiles {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "HandleFunc" {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			pattern, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("invalid route pattern in %s: %v", filename, err)
			}
			method, path, ok := strings.Cut(pattern, " ")
			if !ok {
				t.Fatalf("route pattern %q in %s omits its method", pattern, filename)
			}
			registered[routeContractKey(method, path)] = true
			return true
		})
	}
	for route := range registered {
		if !documented[route] {
			t.Errorf("registered route %s is missing from OpenAPI", route)
		}
	}
}

func TestOpenAPIDocumentsModelRuntimeSecurityAndSchemas(t *testing.T) {
	documentBytes, err := os.ReadFile("../../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(documentBytes, &document); err != nil {
		t.Fatal(err)
	}
	security, ok := document["security"].([]any)
	if !ok || len(security) == 0 {
		t.Fatal("global control-plane bearer security is missing")
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/healthz", "/broker/status", "/v1/chat/completions", "/api/generate"} {
		operations := paths[path].(map[string]any)
		for _, operation := range operations {
			if body, ok := operation.(map[string]any); ok {
				public, exists := body["security"].([]any)
				if !exists || len(public) != 0 {
					t.Errorf("%s must explicitly override security for public model ingress", path)
				}
				if path != "/healthz" {
					servers, exists := body["servers"].([]any)
					if !exists || len(servers) == 0 || !strings.Contains(fmt.Sprint(servers), "11434") {
						t.Errorf("%s must document the separate model-ingress listener", path)
					}
				}
				break
			}
		}
	}
	metrics := paths["/metrics"].(map[string]any)["get"].(map[string]any)
	if _, exists := metrics["security"]; exists {
		t.Error("control /metrics must inherit bearer security")
	}
	controlPort := runtimeControlPublishedPort(t)
	documentedServers := fmt.Sprint(document["servers"])
	if !strings.Contains(documentedServers, ":"+strconv.Itoa(controlPort)) || strings.Contains(documentedServers, ":28080") {
		t.Errorf("control servers must use runtime default published port %d: %s", controlPort, documentedServers)
	}
	servers := fmt.Sprint(metrics["servers"])
	if !strings.Contains(servers, ":"+strconv.Itoa(controlPort)) || strings.Contains(servers, ":28080") || strings.Contains(servers, "11434") {
		t.Errorf("control /metrics must document only the authenticated control listener: %s", servers)
	}
	modelDocumentBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "openapi-model-ingress.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var modelDocument map[string]any
	if err := yaml.Unmarshal(modelDocumentBytes, &modelDocument); err != nil {
		t.Fatal(err)
	}
	modelMetrics := modelDocument["paths"].(map[string]any)["/metrics"].(map[string]any)["get"].(map[string]any)
	public, exists := modelMetrics["security"].([]any)
	if !exists || len(public) != 0 || !strings.Contains(fmt.Sprint(modelDocument["servers"]), "11434") {
		t.Errorf("model /metrics must be public on the model listener: operation=%v servers=%v", modelMetrics, modelDocument["servers"])
	}
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	status := schemas["ModelRequestStatus"].(map[string]any)
	enum := fmt.Sprint(status["enum"])
	for _, required := range []string{"cancel_requested", "dropped"} {
		if !strings.Contains(enum, required) {
			t.Errorf("ModelRequestStatus omits %s", required)
		}
	}
	queue := fmt.Sprint(schemas["ModelQueue"])
	for _, field := range []string{"queued", "running", "cancel_requested", "max_depth", "in_flight_request_ids"} {
		if !strings.Contains(queue, field) {
			t.Errorf("ModelQueue omits %s", field)
		}
	}
	artifactReport := fmt.Sprint(schemas["ArtifactReport"])
	for _, field := range []string{"maxItems:90", "declared_retention_expired", "declared_retention_missing", "retention_semantics", "pruned_bytes_total"} {
		if !strings.Contains(strings.ReplaceAll(artifactReport, " ", ""), strings.ReplaceAll(field, " ", "")) {
			t.Errorf("ArtifactReport omits %s", field)
		}
	}
	responses := components["responses"].(map[string]any)
	if !strings.Contains(fmt.Sprint(responses["ModelBrokerStatusResponse"]), "queue") {
		t.Error("ModelBrokerStatusResponse omits queue")
	}
}

func TestListenerContractsMatchDocumentedRouteMethods(t *testing.T) {
	control := openAPIOperations(t, "../../../docs/openapi.yaml")
	model := openAPIOperations(t, "../../../docs/openapi-model-ingress.yaml")
	shared := map[string]bool{
		"GET /health": true, "GET /healthz": true, "GET /readyz": true, "GET /metrics": true,
	}

	for operation := range control {
		method, path, _ := strings.Cut(operation, " ")
		request := httptest.NewRequest(method, contractRequestPath(path), nil)
		wantModel := model[operation]
		if got := modelIngressRouteAllowed(request); got != wantModel {
			t.Errorf("model listener classification for %s = %t, want %t", operation, got, wantModel)
		}
		wantControl := !wantModel || shared[operation]
		if got := controlPlaneRouteAllowed(request); got != wantControl {
			t.Errorf("control listener classification for %s = %t, want %t", operation, got, wantControl)
		}
	}
	for operation := range model {
		if !control[operation] {
			t.Errorf("model listener operation %s is absent from the complete API contract", operation)
		}
	}

	assertControlHeadAliasPolicy(t)
	routes := NewAPI(NewStore(nil), NewRunner(NewStore(nil))).routes()
	paths := map[string]bool{}
	for operation := range control {
		_, path, _ := strings.Cut(operation, " ")
		paths[path] = true
	}
	for path := range paths {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
			request := httptest.NewRequest(method, contractRequestPath(path), nil)
			candidate := routeContractKey(method, path)
			want := control[candidate]
			if method == http.MethodHead {
				want = control[routeContractKey(http.MethodGet, path)]
			}
			want = want && controlPlaneRouteAllowed(request)
			_, pattern := routes.Handler(request)
			got := pattern != "" && controlPlaneRouteAllowed(request)
			if got != want {
				t.Errorf("control listener route availability for %s = %t, want %t (pattern %q)", candidate, got, want, pattern)
			}
		}
	}

	for _, operation := range []string{
		"GET /api/model-broker/status",
		"GET /api/model-broker/queue",
		"GET /api/model-broker/requests",
		"DELETE /api/model-broker/requests",
		"GET /api/model-broker/requests/{id}",
		"POST /api/model-broker/requests/{id}/cancel",
		"POST /api/model-broker/requests/{id}/drop",
		"GET /api/artifacts/report",
		"GET /api/compatibility/retirement",
		"GET /api/compatibility/report",
		"GET /api/compatibility/observer",
	} {
		method, path, _ := strings.Cut(operation, " ")
		operation = routeContractKey(method, path)
		if !control[operation] {
			t.Errorf("control contract omits issue #27-29 operation %s", operation)
		}
		if model[operation] {
			t.Errorf("model contract exposes control-only operation %s", operation)
		}
	}

	for operation := range model {
		_, path, _ := strings.Cut(operation, " ")
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
			candidate := routeContractKey(method, path)
			request := httptest.NewRequest(method, contractRequestPath(path), nil)
			if got, want := modelIngressRouteAllowed(request), model[candidate]; got != want {
				t.Errorf("model listener classification for %s = %t, want %t", candidate, got, want)
			}
		}
	}

	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "listener-contract-token")
	api := NewAPI(NewStore(nil), NewRunner(NewStore(nil)))
	for _, path := range []string{
		"/api/model-broker/status",
		"/api/model-broker/queue",
		"/api/model-broker/requests",
		"/api/model-broker/requests/contract-id",
		"/api/model-broker/requests/contract-id/cancel",
		"/api/model-broker/requests/contract-id/drop",
		"/api/artifacts/report",
		"/api/compatibility/retirement",
		"/api/compatibility/report",
		"/api/compatibility/observer",
	} {
		request := httptest.NewRequest(http.MethodPut, path, nil)
		request.Header.Set("Authorization", "Bearer listener-contract-token")
		response := httptest.NewRecorder()
		api.ControlPlaneHandler().ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("control listener undocumented method PUT %s status = %d, want %d", path, response.Code, http.StatusMethodNotAllowed)
		}
	}
}

func runtimeControlPublishedPort(t *testing.T) int {
	t.Helper()
	contents, err := os.ReadFile("../runtimeconfig/defaults-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Listeners map[string]struct {
			PublishedDefault int `json:"published_default"`
		} `json:"listeners"`
	}
	if err := json.Unmarshal(contents, &contract); err != nil {
		t.Fatal(err)
	}
	port := contract.Listeners["control"].PublishedDefault
	if port == 0 {
		t.Fatal("runtime defaults contract omits control published_default")
	}
	return port
}

func assertControlHeadAliasPolicy(t *testing.T) {
	t.Helper()
	contents, err := os.ReadFile("../../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		MethodAliases []struct {
			Listener string `yaml:"listener"`
			Source   string `yaml:"source"`
			Alias    string `yaml:"alias"`
			Scope    string `yaml:"scope"`
		} `yaml:"x-switchyard-method-aliases"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.MethodAliases) != 1 {
		t.Fatalf("OpenAPI method aliases = %d, want 1", len(document.MethodAliases))
	}
	alias := document.MethodAliases[0]
	if alias.Listener != "control" || alias.Source != http.MethodGet || alias.Alias != http.MethodHead || alias.Scope != "all operations available on the control listener" {
		t.Errorf("unexpected control HEAD alias policy: %+v", alias)
	}
}

func TestControlListenerHeadUsesReadAuthenticationAndExcludesModelRoutes(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-token")
	store := NewStore(nil)
	handler := NewAPI(store, NewRunner(store)).ControlPlaneHandler()

	tests := []struct {
		name  string
		path  string
		token string
		want  int
	}{
		{name: "authenticated control HEAD", path: "/api/workflows", token: "admin-token", want: http.StatusOK},
		{name: "read token control HEAD", path: "/api/workflows", token: "read-token", want: http.StatusOK},
		{name: "unauthenticated control HEAD", path: "/api/workflows", want: http.StatusUnauthorized},
		{name: "unauthenticated health HEAD", path: "/health", want: http.StatusOK},
		{name: "unauthenticated healthz HEAD", path: "/healthz", want: http.StatusOK},
		{name: "unauthenticated readiness HEAD", path: "/readyz", want: http.StatusOK},
		{name: "model-only HEAD excluded", path: "/v1/models", token: "admin-token", want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodHead, test.path, nil)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Errorf("status = %d, want %d", response.Code, test.want)
			}
		})
	}

	response := httptest.NewRecorder()
	NewAPI(store, NewRunner(store)).ModelIngressHandler().ServeHTTP(
		response,
		httptest.NewRequest(http.MethodHead, "/api/workflows", nil),
	)
	if response.Code != http.StatusNotFound {
		t.Errorf("model listener control-only HEAD status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func openAPIOperations(t *testing.T, path string) map[string]bool {
	t.Helper()
	documentBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(documentBytes, &document); err != nil {
		t.Fatalf("%s is invalid: %v", path, err)
	}
	operations := map[string]bool{}
	for route, methods := range document.Paths {
		for method := range methods {
			switch strings.ToUpper(method) {
			case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				operations[routeContractKey(strings.ToUpper(method), route)] = true
			}
		}
	}
	return operations
}

func TestRunAndTriggerActionRoutesRejectUnsupportedPaths(t *testing.T) {
	store := NewStore(nil)
	mux := NewAPI(store, NewRunner(store)).routes()
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/runs/contract-id/unknown"},
		{http.MethodPost, "/api/runs/contract-id/steps/contract-step/unknown"},
		{http.MethodPost, "/api/triggers/contract-id/unknown"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
			if recorder.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", recorder.Code, http.StatusNotFound)
			}
		})
	}
}

func contractRequestPath(path string) string {
	var builder strings.Builder
	inParameter := false
	for _, character := range path {
		switch character {
		case '{':
			inParameter = true
			builder.WriteString("contract-id")
		case '}':
			inParameter = false
		default:
			if !inParameter {
				builder.WriteRune(character)
			}
		}
	}
	return builder.String()
}

func routeContractKey(method, path string) string {
	var builder strings.Builder
	inParameter := false
	for _, character := range path {
		switch character {
		case '{':
			inParameter = true
			builder.WriteByte('{')
		case '}':
			inParameter = false
			builder.WriteByte('}')
		default:
			if !inParameter {
				builder.WriteRune(character)
			}
		}
	}
	return method + " " + builder.String()
}
