package httpapi

import (
	"net/http"
	"strings"
	"sync/atomic"

	"httpkvdb/internal/auth"
	"httpkvdb/internal/config"
	"httpkvdb/internal/lock"
	"httpkvdb/internal/observe"
	"httpkvdb/internal/storage"
	httptx "httpkvdb/internal/tx"
)

type Server struct {
	cfg     config.Config
	store   *storage.Store
	auth    *auth.Authenticator
	lock    *lock.Serializable
	tx      *httptx.Coordinator
	metrics *observe.Metrics
}

func NewServer(cfg config.Config, store *storage.Store, authn *auth.Authenticator, serial *lock.Serializable, coord *httptx.Coordinator, metrics *observe.Metrics) *Server {
	return &Server{cfg: cfg, store: store, auth: authn, lock: serial, tx: coord, metrics: metrics}
}

func (s *Server) Handler() http.Handler {
	// Dispatch API paths before ServeMux can clean dot segments or repeated slashes.
	// EscapedPath preserves the key's encoding until its single decoding step.
	protected := s.auth.Middleware(http.HandlerFunc(s.v1))
	metrics := s.metrics.Handler()
	handler := observe.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w = headResponseWriter{ResponseWriter: w}
		}
		atomic.AddUint64(&s.metrics.Requests, 1)
		path := r.URL.EscapedPath()
		switch {
		case strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/api/v1/"):
			protected.ServeHTTP(w, r)
		case path == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte("ok\n"))
			}
		case path == "/readyz" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			s.readyz(w, r)
		case path == "/metrics" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			metrics.ServeHTTP(w, r)
		default:
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "route not found")
		}
	}))
	return corsMiddleware(s.cfg.CORSAllowedOrigins, handler)
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ready(); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_ERROR", "storage unavailable")
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) v1(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.EscapedPath()
	if strings.HasPrefix(rawPath, "/api/v1/") {
		s.handleUserspaceKV(w, r, strings.TrimPrefix(rawPath, "/api/v1/"))
		return
	}
	path := strings.TrimPrefix(rawPath, "/v1")
	switch {
	case strings.HasPrefix(path, "/kv/"):
		s.handleKV(w, r, strings.TrimPrefix(path, "/kv/"))
	case strings.HasPrefix(path, "/admin/"):
		s.handleAdmin(w, r, strings.TrimPrefix(path, "/admin/"))
	case path == "/tx" && r.Method == http.MethodPost:
		s.handleCreateTx(w, r)
	case strings.HasPrefix(path, "/tx/"):
		s.handleTx(w, r, strings.TrimPrefix(path, "/tx/"))
	case path == "/export" && r.Method == http.MethodGet:
		s.handleExport(w, r)
	case path == "/import" && r.Method == http.MethodPost:
		s.handleImport(w, r)
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

// HEAD includes response headers and status while suppressing all response bodies.
type headResponseWriter struct{ http.ResponseWriter }

func (w headResponseWriter) Write(body []byte) (int, error) { return len(body), nil }
