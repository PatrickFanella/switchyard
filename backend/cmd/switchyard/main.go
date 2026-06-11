package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
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
	if len(args) < 2 || args[0] != "open-pilot" {
		return usage()
	}
	switch args[1] {
	case "run":
		return openPilotRun(args[2:], out)
	case "cancel":
		return openPilotCancel(args[2:], out)
	default:
		return usage()
	}
}

func usage() error {
	return fmt.Errorf("usage: switchyard open-pilot run --repo OWNER/REPO --issue 123 --json | switchyard open-pilot cancel --run RUN_ID --json")
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

func defaultAPIURL() string {
	if value := os.Getenv("SWITCHYARD_API_URL"); value != "" {
		return value
	}
	return "http://127.0.0.1:8080"
}
