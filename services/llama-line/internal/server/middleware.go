package server

import (
	"context"
	"net/http"
	"strings"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/keystore"
)

type contextKey string

const (
	ClientNameKey        contextKey = "client_name"
	ClientPriorityKey    contextKey = "client_priority"
	ClientDeduplicateKey contextKey = "client_deduplicate"
	ClientCacheTTLKey    contextKey = "client_cache_ttl_secs"
	ClientCacheMaxKey    contextKey = "client_cache_max_entries"
)

// AuthMiddleware validates Bearer tokens against the key store and attaches
// client name, priority, and dedup/cache settings to the request context.
// Returns 401 on missing or invalid token.
func AuthMiddleware(ks *keystore.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token, found := strings.CutPrefix(auth, "Bearer ")
		if !found || token == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", 0, 0)
			return
		}
		k := ks.Lookup(token)
		if k == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", 0, 0)
			return
		}
		ctx := context.WithValue(r.Context(), ClientNameKey, k.Name)
		ctx = context.WithValue(ctx, ClientPriorityKey, k.Priority)
		ctx = context.WithValue(ctx, ClientDeduplicateKey, k.Deduplicate)
		ctx = context.WithValue(ctx, ClientCacheTTLKey, k.CacheTTLSecs)
		ctx = context.WithValue(ctx, ClientCacheMaxKey, k.CacheMaxEntries)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AuthMiddlewareFromKeys is a convenience wrapper that builds a keystore from
// a config key slice. Useful in tests and legacy call sites.
func AuthMiddlewareFromKeys(keys []config.APIKey, next http.Handler) http.Handler {
	return AuthMiddleware(keystore.New(keys), next)
}

// clientNameFromCtx extracts the client name from context, or returns "unknown".
func clientNameFromCtx(ctx context.Context) string {
	if name, ok := ctx.Value(ClientNameKey).(string); ok {
		return name
	}
	return "unknown"
}

// clientPriorityFromCtx extracts the client priority from context, or returns 0.
func clientPriorityFromCtx(ctx context.Context) int {
	if p, ok := ctx.Value(ClientPriorityKey).(int); ok {
		return p
	}
	return 0
}

// clientDeduplicateFromCtx reports whether deduplication is enabled for this client.
func clientDeduplicateFromCtx(ctx context.Context) bool {
	if v, ok := ctx.Value(ClientDeduplicateKey).(bool); ok {
		return v
	}
	return false
}

// clientCacheTTLFromCtx returns the cache TTL in seconds for this client (0 = disabled).
func clientCacheTTLFromCtx(ctx context.Context) int {
	if v, ok := ctx.Value(ClientCacheTTLKey).(int); ok {
		return v
	}
	return 0
}

// clientCacheMaxFromCtx returns the cache max entries for this client (0 = use global default).
func clientCacheMaxFromCtx(ctx context.Context) int {
	if v, ok := ctx.Value(ClientCacheMaxKey).(int); ok {
		return v
	}
	return 0
}
