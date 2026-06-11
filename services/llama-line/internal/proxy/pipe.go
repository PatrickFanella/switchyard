package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// PipeResponse copies all headers from resp to w, injects X-Ollama-Broker: true,
// writes the status code (unless headersAlreadySent is true), then streams the body to w.
//
// When headersAlreadySent is true (SSE mode):
//   - If upstream Content-Type is text/event-stream, chunks are passed through as-is.
//   - Otherwise the upstream body is scanned line-by-line and each non-empty line is
//     emitted as its own SSE `data: <line>\n\n` event with flush. This correctly handles
//     ollama 's application/x-ndjson streaming chat/generate responses (one SSE event
//     per chunk) as well as single-line JSON responses (one SSE event).
//
// When headersAlreadySent is false, the body is written as-is (bare JSON or stream).
//
// Flushes after each write if w implements http.Flusher.
func PipeResponse(w http.ResponseWriter, resp *http.Response, headersAlreadySent bool) error {
	upstreamIsSSE := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")

	if !headersAlreadySent {
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("X-Ollama-Broker", "true")
		w.WriteHeader(resp.StatusCode)
	}

	flusher, canFlush := w.(http.Flusher)

	if headersAlreadySent && !upstreamIsSSE {
		// Wrap each upstream JSON line as its own SSE data event so streaming
		// (application/x-ndjson) is preserved end-to-end.
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			if _, werr := fmt.Fprintf(w, "data: %s\n\n", line); werr != nil {
				return werr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		return nil
	}

	// Streaming upstream (already SSE) or non-SSE mode: pass through as-is.
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}
