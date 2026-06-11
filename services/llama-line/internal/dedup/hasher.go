// Package dedup provides request deduplication and response caching for the llama-line broker.
// Deduplication allows multiple identical in-flight requests to share a single ollama call.
// Caching allows repeated non-streaming requests to be served from memory without hitting ollama.
package dedup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// requestBody is the minimal subset of an ollama/OpenAI request used for hashing.
// Only model and messages are included; temperature, seed, etc. are intentionally excluded
// because they are optimization hints, not content-defining fields.
type requestBody struct {
	Model    string          `json:"model"`
	Messages json.RawMessage `json:"messages"`
}

// HashRequest returns a hex-encoded SHA256 hash of the (model, messages) pair
// extracted from a raw JSON request body.
// Returns ("", false) if body is not valid JSON or is missing the required fields.
func HashRequest(body []byte) (string, bool) {
	var rb requestBody
	if err := json.Unmarshal(body, &rb); err != nil {
		return "", false
	}
	if rb.Model == "" || len(rb.Messages) == 0 {
		return "", false
	}

	h := sha256.New()
	h.Write([]byte(rb.Model))
	h.Write([]byte("\x00")) // separator
	h.Write(rb.Messages)
	return hex.EncodeToString(h.Sum(nil)), true
}
