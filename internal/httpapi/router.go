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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle("GET /metrics", s.metrics.Handler())
	protected := s.auth.Middleware(http.HandlerFunc(s.v1))
	mux.Handle("/v1/", protected)
	mux.Handle("/api/v1/", protected)
	handler := observe.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddUint64(&s.metrics.Requests, 1)
		mux.ServeHTTP(w, r)
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
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	if strings.HasPrefix(r.URL.Path, "/api/v1/") {
		path = strings.TrimPrefix(r.URL.Path, "/api/v1")
	}
	switch {
	case strings.HasPrefix(path, "/kv/"):
		s.handleKV(w, r, strings.TrimPrefix(path, "/kv/"))
	case strings.HasPrefix(r.URL.Path, "/v1/admin/"):
		s.handleAdmin(w, r, strings.TrimPrefix(path, "/admin/"))
	case path == "/tx" && r.Method == http.MethodPost:
		s.handleCreateTx(w, r)
	case strings.HasPrefix(path, "/tx/"):
		s.handleTx(w, r, strings.TrimPrefix(path, "/tx/"))
	case path == "/export" && r.Method == http.MethodGet:
		s.handleExport(w, r)
	case path == "/import" && r.Method == http.MethodPost:
		s.handleImport(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v1/"):
		s.handleUserspaceKV(w, r, strings.TrimPrefix(path, "/"))
	default:
		http.NotFound(w, r)
	}
}
