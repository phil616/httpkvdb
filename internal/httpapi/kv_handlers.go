package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"httpkvdb/internal/auth"
	"httpkvdb/internal/storage"
)

func (s *Server) handleUserspaceKV(w http.ResponseWriter, r *http.Request, rawPath string) {
	rawUserspaceID, rawKey, ok := strings.Cut(rawPath, "/")
	if !ok || rawUserspaceID == "" || rawKey == "" {
		http.NotFound(w, r)
		return
	}
	userspaceID, err := url.PathUnescape(rawUserspaceID)
	if err != nil || storage.ValidateUserspaceID(userspaceID) != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_USERSPACE", "invalid userspace")
		return
	}
	p, _ := auth.PrincipalFromContext(r.Context())
	if p.UserspaceID != userspaceID {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "userspace forbidden")
		return
	}
	s.handleKV(w, r, rawKey)
}

func (s *Server) handleKV(w http.ResponseWriter, r *http.Request, rawKey string) {
	key, err := url.PathUnescape(rawKey)
	if err != nil || storage.ValidateKey(key, s.cfg.MaxKeySize) != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_KEY", "invalid key")
		return
	}
	p, _ := auth.PrincipalFromContext(r.Context())
	switch r.Method {
	case http.MethodPut:
		s.handlePut(w, r, p.UserspaceID, key)
	case http.MethodGet:
		s.handleGet(w, r, p.UserspaceID, key, false)
	case http.MethodHead:
		s.handleGet(w, r, p.UserspaceID, key, true)
	case http.MethodDelete:
		s.handleDelete(w, r, p.UserspaceID, key)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, userspaceID, key string) {
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	if err := storage.ValidateValue(ct, body); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "INVALID_JSON", "invalid json")
		return
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	version, err := s.store.Put(userspaceID, key, body, ct)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		return
	}
	atomic.AddUint64(&s.metrics.KVPut, 1)
	w.Header().Set("X-KV-Version", strconv.FormatUint(version, 10))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, userspaceID, key string, head bool) {
	s.lock.Lock()
	defer s.lock.Unlock()
	rec, err := s.store.Get(userspaceID, key)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "KEY_NOT_FOUND", "key not found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		return
	}
	atomic.AddUint64(&s.metrics.KVGet, 1)
	w.Header().Set("Content-Type", rec.ContentType)
	w.Header().Set("X-KV-Version", strconv.FormatUint(rec.Version, 10))
	w.Header().Set("X-KV-Size", strconv.Itoa(len(rec.Value)))
	w.Header().Set("X-KV-Checksum", rec.Checksum)
	w.WriteHeader(http.StatusOK)
	if !head {
		_, _ = w.Write(rec.Value)
	}
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, userspaceID, key string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	_, err := s.store.Delete(userspaceID, key)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "KEY_NOT_FOUND", "key not found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		return
	}
	atomic.AddUint64(&s.metrics.KVDelete, 1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxValueSize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "VALUE_TOO_LARGE", "value too large")
		return nil, false
	}
	return body, true
}
