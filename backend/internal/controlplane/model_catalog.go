package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type modelCatalogResponse struct {
	Ollama    providerModelCatalog `json:"ollama"`
	OpenCode  providerModelCatalog `json:"opencode"`
	CheckedAt time.Time            `json:"checked_at"`
}

type providerModelCatalog struct {
	Configured bool             `json:"configured"`
	BaseURL    string           `json:"base_url,omitempty"`
	Status     string           `json:"status"`
	Models     []modelCatalogID `json:"models"`
	Providers  []map[string]any `json:"providers,omitempty"`
	Error      string           `json:"error,omitempty"`
}

type modelCatalogID struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Provider string         `json:"provider"`
	Details  map[string]any `json:"details,omitempty"`
}

type modelPullRequest struct {
	Provider string   `json:"provider"`
	Models   []string `json:"models"`
}

type modelPullResponse struct {
	Provider string            `json:"provider"`
	Results  []modelPullResult `json:"results"`
	Count    int               `json:"count"`
}

type modelPullResult struct {
	Model  string         `json:"model"`
	Status string         `json:"status"`
	Detail map[string]any `json:"detail,omitempty"`
	Error  string         `json:"error,omitempty"`
}

func (a *API) modelCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, modelCatalogResponse{
		Ollama:    ollamaModelCatalog(r),
		OpenCode:  openCodeModelCatalog(r),
		CheckedAt: time.Now().UTC(),
	})
}

func (a *API) pullModels(w http.ResponseWriter, r *http.Request) {
	var input modelPullRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider := strings.ToLower(strings.TrimSpace(firstNonEmpty(input.Provider, "ollama")))
	if provider != "ollama" {
		writeError(w, http.StatusBadRequest, "only ollama model pulls are supported; OpenCode can refresh model metadata with `opencode models --refresh`")
		return
	}
	models := normalizeModelList(input.Models)
	if len(models) == 0 {
		models = normalizeModelList(strings.Split(os.Getenv("OLLAMA_PULL_MODELS"), ","))
	}
	if len(models) == 0 {
		writeError(w, http.StatusBadRequest, "models are required; pass JSON {\"models\":[\"name:tag\"]} or set OLLAMA_PULL_MODELS")
		return
	}
	baseURL := strings.TrimRight(os.Getenv("OLLAMA_BASE_URL"), "/")
	if baseURL == "" {
		writeError(w, http.StatusBadGateway, "OLLAMA_BASE_URL is not configured")
		return
	}

	results := make([]modelPullResult, 0, len(models))
	status := http.StatusOK
	for _, model := range models {
		result := pullOllamaModel(r, baseURL, model)
		if result.Error != "" {
			status = http.StatusBadGateway
		}
		results = append(results, result)
	}
	writeJSON(w, status, modelPullResponse{Provider: "ollama", Results: results, Count: len(results)})
}

func ollamaModelCatalog(r *http.Request) providerModelCatalog {
	baseURL := strings.TrimRight(os.Getenv("OLLAMA_BASE_URL"), "/")
	catalog := providerModelCatalog{Configured: baseURL != "", BaseURL: baseURL, Status: "not_configured", Models: []modelCatalogID{}}
	if baseURL == "" {
		catalog.Error = "OLLAMA_BASE_URL is not configured"
		return catalog
	}
	response, err := fetchJSONMap(r, baseURL, "/api/tags", http.MethodGet, nil, "", "")
	if err != nil {
		catalog.Status = "failed"
		catalog.Error = err.Error()
		return catalog
	}
	catalog.Status = "healthy"
	if models, ok := response["models"].([]any); ok {
		for _, item := range models {
			model, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name := firstString(model["model"], model["name"])
			if name == "" {
				continue
			}
			catalog.Models = append(catalog.Models, modelCatalogID{ID: name, Name: name, Provider: "ollama", Details: model})
		}
	}
	return catalog
}

func openCodeModelCatalog(r *http.Request) providerModelCatalog {
	baseURL := strings.TrimRight(firstNonEmpty(os.Getenv("OPENCODE_SERVER_URL"), "http://opencode:4096"), "/")
	catalog := providerModelCatalog{Configured: baseURL != "", BaseURL: baseURL, Status: "not_configured", Models: []modelCatalogID{}, Providers: []map[string]any{}}
	if baseURL == "" {
		catalog.Error = "OPENCODE_SERVER_URL is not configured"
		return catalog
	}
	username := os.Getenv("OPENCODE_SERVER_USERNAME")
	password := os.Getenv("OPENCODE_SERVER_PASSWORD")
	response, err := fetchJSONMap(r, baseURL, "/config/providers", http.MethodGet, nil, username, password)
	if err != nil {
		// Older OpenCode builds may expose /provider instead.
		response, err = fetchJSONMap(r, baseURL, "/provider", http.MethodGet, nil, username, password)
	}
	if err != nil {
		catalog.Status = "failed"
		catalog.Error = err.Error()
		return catalog
	}
	catalog.Status = "healthy"
	catalog.Providers = extractProviderMaps(response)
	catalog.Models = extractOpenCodeModels(response)
	return catalog
}

func fetchJSONMap(r *http.Request, baseURL, path, method string, body []byte, username, password string) (map[string]any, error) {
	endpoint, err := url.JoinPath(baseURL, path)
	if err != nil {
		return nil, err
	}
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if username != "" || password != "" {
		req.SetBasicAuth(firstNonEmpty(username, "switchyard"), password)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("upstream returned %s", resp.Status)
	}
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func pullOllamaModel(r *http.Request, baseURL, model string) modelPullResult {
	name := ollamaModelName(model)
	body, _ := json.Marshal(map[string]any{"name": name, "stream": false})
	response, err := fetchJSONMap(r, baseURL, "/api/pull", http.MethodPost, body, "", "")
	if err != nil {
		return modelPullResult{Model: name, Status: "failed", Error: err.Error()}
	}
	return modelPullResult{Model: name, Status: firstNonEmpty(firstString(response["status"]), "pulled"), Detail: response}
}

func normalizeModelList(models []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range models {
		for _, part := range strings.Split(raw, ",") {
			model := strings.TrimSpace(part)
			if model == "" || seen[model] {
				continue
			}
			seen[model] = true
			out = append(out, model)
		}
	}
	return out
}

func extractProviderMaps(response map[string]any) []map[string]any {
	providers := []map[string]any{}
	for _, key := range []string{"providers", "data"} {
		if items, ok := response[key].([]any); ok {
			for _, item := range items {
				if provider, ok := item.(map[string]any); ok {
					providers = append(providers, provider)
				}
			}
		}
	}
	if len(providers) == 0 {
		for key, value := range response {
			if provider, ok := value.(map[string]any); ok && key != "models" {
				provider["id"] = firstNonEmpty(firstString(provider["id"]), key)
				providers = append(providers, provider)
			}
		}
	}
	return providers
}

func extractOpenCodeModels(response map[string]any) []modelCatalogID {
	models := []modelCatalogID{}
	seen := map[string]bool{}
	var walk func(provider string, value any)
	walk = func(provider string, value any) {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				walk(provider, item)
			}
		case map[string]any:
			providerName := firstNonEmpty(firstString(typed["provider"], typed["providerID"], typed["provider_id"]), provider)
			if id := firstString(typed["id"], typed["model"], typed["name"]); id != "" {
				key := providerName + "/" + id
				if !seen[key] {
					seen[key] = true
					models = append(models, modelCatalogID{ID: id, Name: firstNonEmpty(firstString(typed["name"]), id), Provider: providerName, Details: typed})
				}
			}
			for key, nested := range typed {
				if key == "models" || key == "model" || key == "data" {
					walk(providerName, nested)
				}
			}
		}
	}
	walk("opencode", response["models"])
	for _, provider := range extractProviderMaps(response) {
		walk(firstString(provider["id"], provider["name"]), provider["models"])
	}
	return models
}

func firstString(values ...any) string {
	for _, value := range values {
		if str, ok := value.(string); ok && strings.TrimSpace(str) != "" {
			return strings.TrimSpace(str)
		}
	}
	return ""
}
