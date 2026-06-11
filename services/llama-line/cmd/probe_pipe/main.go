package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"

	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
)

type capWriter struct {
	hdr http.Header
	w   *os.File
}

func (c *capWriter) Header() http.Header { return c.hdr }
func (c *capWriter) Write(p []byte) (int, error) {
	fmt.Fprintf(os.Stderr, "[WRITE %d bytes]\n", len(p))
	return c.w.Write(p)
}
func (c *capWriter) WriteHeader(int) {}
func (c *capWriter) Flush()          { fmt.Fprintln(os.Stderr, "[FLUSH]") }

func main() {
	body := bytes.NewReader([]byte(`{"model":"qwen3:14b","messages":[{"role":"user","content":"Say pong"}],"stream":true,"think":false}`))
	req, _ := http.NewRequest("POST", "http://127.0.0.1:11435/api/chat", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "upstream Content-Type=%q\n", resp.Header.Get("Content-Type"))
	w := &capWriter{hdr: http.Header{}, w: os.Stdout}
	if err := proxy.PipeResponse(w, resp, true); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintln(os.Stderr, "[DONE]")
}
