package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const worktreeRoot = "/workspace/.open-pilot-worktrees"

type verifyRequest struct {
	Command []string `json:"command"`
	Workdir string   `json:"workdir"`
	Timeout string   `json:"timeout"`
}

type verifyResponse struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /verify", verify)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := &http.Server{Addr: ":8090", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("verifier stopped", "error", err)
		os.Exit(1)
	}
}

func verify(w http.ResponseWriter, r *http.Request) {
	var request verifyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		writeResponse(w, http.StatusBadRequest, verifyResponse{Error: "invalid request"})
		return
	}
	if len(request.Command) == 0 || strings.TrimSpace(request.Command[0]) == "" {
		writeResponse(w, http.StatusBadRequest, verifyResponse{Error: "command is required"})
		return
	}
	workdir, err := safeWorkdir(request.Workdir)
	if err != nil {
		writeResponse(w, http.StatusBadRequest, verifyResponse{Error: err.Error()})
		return
	}
	timeout, err := time.ParseDuration(request.Timeout)
	if err != nil || timeout <= 0 || timeout > 10*time.Minute {
		writeResponse(w, http.StatusBadRequest, verifyResponse{Error: "invalid timeout"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, request.Command[0], request.Command[1:]...)
	command.Dir = workdir
	output, err := command.CombinedOutput()
	response := verifyResponse{Output: strings.TrimSpace(string(output))}
	if err != nil {
		response.Error = "command failed"
		writeResponse(w, http.StatusUnprocessableEntity, response)
		return
	}
	writeResponse(w, http.StatusOK, response)
}

func safeWorkdir(workdir string) (string, error) {
	cleaned := filepath.Clean(workdir)
	relative, err := filepath.Rel(worktreeRoot, cleaned)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return "", errors.New("workdir must be an Open Pilot worktree")
	}
	return cleaned, nil
}

func writeResponse(w http.ResponseWriter, status int, response verifyResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
