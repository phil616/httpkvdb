package httpapi

import (
	"net/http"
	"strings"
)

const corsAllowedHeaders = "Authorization, Content-Type, Accept, X-Request-Id, X-KV-Op, X-KV-Key, X-KV-Op-Id, X-KV-Import-Mode"
const corsExposedHeaders = "X-Request-Id, X-KV-Version, X-KV-Size, X-KV-Checksum, Content-Disposition"
const corsAllowedMethods = "GET, HEAD, POST, PUT, DELETE, OPTIONS"

func corsMiddleware(allowedOrigins string, next http.Handler) http.Handler {
	origins := parseOrigins(allowedOrigins)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if allowed, value := allowedOrigin(origin, origins); allowed {
				w.Header().Set("Access-Control-Allow-Origin", value)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", corsAllowedMethods)
				w.Header().Set("Access-Control-Allow-Headers", corsAllowedHeaders)
				w.Header().Set("Access-Control-Expose-Headers", corsExposedHeaders)
				w.Header().Set("Access-Control-Max-Age", "600")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseOrigins(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(item)
		if origin != "" {
			out[origin] = struct{}{}
		}
	}
	return out
}

func allowedOrigin(origin string, allowed map[string]struct{}) (bool, string) {
	if _, ok := allowed["*"]; ok {
		return true, "*"
	}
	if _, ok := allowed[origin]; ok {
		return true, origin
	}
	return false, ""
}
