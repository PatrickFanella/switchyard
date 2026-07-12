package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) < 2 {
		return usage()
	}
	switch args[0] {
	case "open-pilot":
		switch args[1] {
		case "run":
			return openPilotRun(args[2:], out)
		case "cancel":
			return openPilotCancel(args[2:], out)
		default:
			return usage()
		}
	case "models":
		switch args[1] {
		case "list":
			return modelsList(args[2:], out)
		case "pull":
			return modelsPull(args[2:], out)
		case "refresh-opencode":
			return modelsRefreshOpenCode(args[2:], out)
		default:
			return usage()
		}
	}
	return usage()
}

func usage() error {
	return fmt.Errorf("usage: switchyard open-pilot run --repo OWNER/REPO --issue 123 --json | switchyard open-pilot cancel --run RUN_ID --json | switchyard models list --json | switchyard models pull --models llama3.2:latest,qwen3.5:latest --json | switchyard models refresh-opencode")
}

func openPilotRun(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("open-pilot run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repo := flags.String("repo", "", "repository owner/name")
	issue := flags.String("issue", "", "issue number")
	jsonOut := flags.Bool("json", false, "print JSON response")
	apiURL := flags.String("api-url", defaultAPIURL(), "Switchyard API URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repo == "" || *issue == "" {
		return fmt.Errorf("repo and issue are required")
	}
	body, _ := json.Marshal(map[string]string{"repo": *repo, "issue": *issue})
	return postJSON(*apiURL, "/api/workflows/open-pilot/run", body, *jsonOut, out)
}

func openPilotCancel(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("open-pilot cancel", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runID := flags.String("run", "", "Switchyard run ID")
	jsonOut := flags.Bool("json", false, "print JSON response")
	apiURL := flags.String("api-url", defaultAPIURL(), "Switchyard API URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *runID == "" {
		return fmt.Errorf("run is required")
	}
	return postJSON(*apiURL, "/api/runs/"+*runID+"/cancel", nil, *jsonOut, out)
}

func modelsList(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("models list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOut := flags.Bool("json", false, "print JSON response")
	apiURL := flags.String("api-url", defaultAPIURL(), "Switchyard API URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return getJSON(*apiURL, "/api/models", *jsonOut, out)
}

func modelsPull(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("models pull", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	models := flags.String("models", os.Getenv("OLLAMA_PULL_MODELS"), "comma-separated Ollama model names to pull")
	jsonOut := flags.Bool("json", false, "print JSON response")
	apiURL := flags.String("api-url", defaultAPIURL(), "Switchyard API URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	list := splitCSV(*models)
	if len(list) == 0 {
		return fmt.Errorf("models are required; pass --models or set OLLAMA_PULL_MODELS")
	}
	body, _ := json.Marshal(map[string]any{"provider": "ollama", "models": list})
	return postJSON(*apiURL, "/api/models/pull", body, *jsonOut, out)
}

func modelsRefreshOpenCode(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("models refresh-opencode", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOut := flags.Bool("json", false, "print JSON response")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cmd := exec.Command("opencode", "models", "--refresh")
	data, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("opencode models --refresh failed: %w: %s", err, strings.TrimSpace(string(data)))
	}
	if *jsonOut {
		response, _ := json.Marshal(map[string]any{"provider": "opencode", "status": "refreshed", "output": strings.TrimSpace(string(data))})
		_, _ = out.Write(append(response, '\n'))
		return nil
	}
	_, _ = fmt.Fprintln(out, "opencode refreshed")
	if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
		_, _ = fmt.Fprintln(out, trimmed)
	}
	return nil
}

func getJSON(apiURL string, path string, jsonOut bool, out io.Writer) error {
	client := &http.Client{Timeout: 30 * time.Second}
	base := strings.TrimRight(apiURL, "/")
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	setControlPlaneAuthorization(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("switchyard API returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if jsonOut {
		_, _ = out.Write(data)
		return nil
	}
	var decoded struct {
		Ollama struct {
			Status string `json:"status"`
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"ollama"`
		OpenCode struct {
			Status string `json:"status"`
			Models []struct {
				ID       string `json:"id"`
				Provider string `json:"provider"`
			} `json:"models"`
		} `json:"opencode"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		_, _ = out.Write(data)
		return nil
	}
	_, _ = fmt.Fprintf(out, "ollama %s (%d models)\n", decoded.Ollama.Status, len(decoded.Ollama.Models))
	for _, model := range decoded.Ollama.Models {
		_, _ = fmt.Fprintf(out, "  ollama/%s\n", model.ID)
	}
	_, _ = fmt.Fprintf(out, "opencode %s (%d models)\n", decoded.OpenCode.Status, len(decoded.OpenCode.Models))
	for _, model := range decoded.OpenCode.Models {
		prefix := model.Provider
		if prefix == "" {
			prefix = "opencode"
		}
		_, _ = fmt.Fprintf(out, "  %s/%s\n", prefix, model.ID)
	}
	return nil
}

func postJSON(apiURL string, path string, body []byte, jsonOut bool, out io.Writer) error {
	client := &http.Client{Timeout: 30 * time.Second}
	base := strings.TrimRight(apiURL, "/")
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	setControlPlaneAuthorization(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("switchyard API returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if jsonOut {
		_, _ = out.Write(data)
		return nil
	}
	var pull struct {
		Results []struct {
			Model  string `json:"model"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &pull); err == nil && len(pull.Results) > 0 {
		for _, result := range pull.Results {
			if result.Error != "" {
				_, _ = fmt.Fprintf(out, "%s failed %s\n", result.Model, result.Error)
				continue
			}
			_, _ = fmt.Fprintf(out, "%s %s\n", result.Model, result.Status)
		}
		return nil
	}
	var decoded struct {
		Run struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"run"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Run.ID == "" {
		_, _ = out.Write(data)
		return nil
	}
	_, _ = fmt.Fprintf(out, "%s %s\n", decoded.Run.ID, decoded.Run.Status)
	return nil
}

func setControlPlaneAuthorization(req *http.Request) {
	if token := strings.TrimSpace(os.Getenv("SWITCHYARD_CONTROL_API_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := []string{}
	seen := map[string]bool{}
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func defaultAPIURL() string {
	if value := os.Getenv("SWITCHYARD_API_URL"); value != "" {
		return value
	}
	return "http://127.0.0.1:8080"
}
