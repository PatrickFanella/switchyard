// Package router maps model names to upstream indices using glob patterns.
// Each upstream may declare a list of model patterns; the first upstream whose
// pattern matches the requested model wins. An upstream with an empty Models
// list is the catch-all fallback.
package router

import (
	"path/filepath"
	"strings"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

// Router resolves a model name to an upstream index.
type Router struct {
	upstreams []config.UpstreamConfig
	fallback  int // index of the catch-all upstream, or -1
}

// New builds a Router from the resolved upstream list.
// The first upstream with an empty Models list is treated as the fallback.
// If no fallback exists, upstream 0 is used.
func New(upstreams []config.UpstreamConfig) *Router {
	fallback := -1
	for i, u := range upstreams {
		if len(u.Models) == 0 {
			fallback = i
			break
		}
	}
	if fallback == -1 && len(upstreams) > 0 {
		fallback = 0
	}
	return &Router{upstreams: upstreams, fallback: fallback}
}

// Resolve returns the index of the upstream that should handle the given model.
// Matching is case-insensitive glob (e.g. "llama3*", "mistral:7b").
// Returns the fallback index when no pattern matches.
func (r *Router) Resolve(model string) int {
	lower := strings.ToLower(model)
	for i, u := range r.upstreams {
		for _, pat := range u.Models {
			matched, err := filepath.Match(strings.ToLower(pat), lower)
			if err == nil && matched {
				return i
			}
		}
	}
	return r.fallback
}

// Len returns the number of upstreams.
func (r *Router) Len() int { return len(r.upstreams) }

// Upstream returns the upstream config at index i.
func (r *Router) Upstream(i int) config.UpstreamConfig { return r.upstreams[i] }

// FallbackIndex returns the fallback upstream index.
func (r *Router) FallbackIndex() int { return r.fallback }
