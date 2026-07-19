package controlplane

import (
	"net/http"
	"os"
	"strings"
)

func withCORS(next http.Handler) http.Handler {
	allowedOrigins := map[string]struct{}{}
	for _, origin := range strings.Split(os.Getenv("SWITCHYARD_CORS_ALLOWED_ORIGINS"), ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowedOrigins[origin] = struct{}{}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if _, ok := allowedOrigins[origin]; ok && origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "content-type, authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			if origin != "" {
				if _, ok := allowedOrigins[origin]; !ok {
					writeError(w, http.StatusForbidden, "origin not allowed")
					return
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
