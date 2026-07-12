package controlplane

import (
	"net/http"
	"os"
	"regexp"
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
			requestPath := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "contract-id")
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

	source, err := os.ReadFile("http.go")
	if err != nil {
		t.Fatal(err)
	}
	registeredPattern := regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]+)"`)
	for _, match := range registeredPattern.FindAllStringSubmatch(string(source), -1) {
		method, registeredPath := match[1], match[2]
		documented := false
		for path, operations := range document.Paths {
			if _, ok := operations[strings.ToLower(method)]; !ok {
				continue
			}
			staticPrefix := strings.Split(path, "{")[0]
			if path == registeredPath || (strings.HasSuffix(registeredPath, "/") && strings.HasPrefix(staticPrefix, registeredPath)) {
				documented = true
				break
			}
		}
		if !documented {
			t.Errorf("registered route %s %s is missing from OpenAPI", method, registeredPath)
		}
	}
}
