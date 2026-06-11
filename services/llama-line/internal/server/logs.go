package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"
)

// HandleAdminLogsRecent returns the most recent broadcaster records as JSON.
// GET /admin/logs/recent?n=200
func (s *Server) HandleAdminLogsRecent(w http.ResponseWriter, r *http.Request) {
	if s.logBroadcaster == nil {
		writeError(w, http.StatusServiceUnavailable, "log broadcaster not configured", 0, 0)
		return
	}
	n := parseN(r.URL.Query().Get("n"), 200)
	recs := s.logBroadcaster.Recent(n)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"entries": recs})
}

// HandleAdminLogsStream streams new log records as SSE.
// GET /admin/logs/stream
func (s *Server) HandleAdminLogsStream(w http.ResponseWriter, r *http.Request) {
	if s.logBroadcaster == nil {
		writeError(w, http.StatusServiceUnavailable, "log broadcaster not configured", 0, 0)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, unsubscribe := s.logBroadcaster.Subscribe()
	defer unsubscribe()

	// Heartbeat keeps proxies/clients from timing out.
	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case rec, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(rec)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
			flusher.Flush()
		case <-hb.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// HandleAdminAuditRecent returns the last n lines of the audit log file as JSON entries.
// GET /admin/audit/recent?n=200
func (s *Server) HandleAdminAuditRecent(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuditLogPath == "" {
		writeError(w, http.StatusServiceUnavailable, "audit log not configured", 0, 0)
		return
	}
	n := parseN(r.URL.Query().Get("n"), 200)
	lines, err := tailLines(s.cfg.AuditLogPath, n)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read audit log: "+err.Error(), 0, 0)
		return
	}
	entries := make([]json.RawMessage, 0, len(lines))
	for _, ln := range lines {
		entries = append(entries, json.RawMessage(ln))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"entries": entries})
}

// HandleAdminAuditStream tails the audit log file and pushes new entries via SSE.
// GET /admin/audit/stream
func (s *Server) HandleAdminAuditStream(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuditLogPath == "" {
		writeError(w, http.StatusServiceUnavailable, "audit log not configured", 0, 0)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(s.cfg.AuditLogPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open audit log: "+err.Error(), 0, 0)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Seek to end so we only stream new entries.
	if _, err := f.Seek(0, 2); err != nil {
		return
	}

	reader := bufio.NewReader(f)
	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()

	var pending []byte
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			for {
				chunk, err := reader.ReadBytes('\n')
				if len(chunk) > 0 {
					pending = append(pending, chunk...)
					if chunk[len(chunk)-1] == '\n' {
						line := pending[:len(pending)-1]
						pending = nil
						if len(line) == 0 {
							continue
						}
						if _, werr := fmt.Fprintf(w, "data: %s\n\n", line); werr != nil {
							return
						}
						flusher.Flush()
					}
				}
				if err != nil {
					break
				}
			}
		}
	}
}

// parseN extracts a positive int from a query string, falling back to def.
func parseN(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return def
	}
	if v > 5000 {
		v = 5000
	}
	return v
}

// tailLines returns up to n trailing newline-delimited lines from path.
// It uses a simple full read for simplicity and clarity; audit logs are
// rotated externally and tail is intended for back-fill only.
func tailLines(path string, n int) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	ring := make([][]byte, 0, n)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}
		if len(ring) < n {
			ring = append(ring, line)
		} else {
			copy(ring, ring[1:])
			ring[n-1] = line
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return ring, nil
}
