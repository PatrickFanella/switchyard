package controlplane

import (
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
	servers := fmt.Sprint(metrics["servers"])
	if !strings.Contains(servers, "28080") || strings.Contains(servers, "11434") {
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
	responses := components["responses"].(map[string]any)
	if !strings.Contains(fmt.Sprint(responses["ModelBrokerStatusResponse"]), "queue") {
		t.Error("ModelBrokerStatusResponse omits queue")
	}
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
