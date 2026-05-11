package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"httpkvdb/internal/auth"
	"httpkvdb/internal/model"
	"httpkvdb/internal/storage"
)

type createUserspaceRequest struct {
	UserID      string `json:"user_id"`
	UserspaceID string `json:"userspace_id"`
}

type createUserspaceResponse struct {
	UserID      string `json:"user_id"`
	UserspaceID string `json:"userspace_id"`
	APIKey      string `json:"api_key"`
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request, rawPath string) {
	p, _ := auth.PrincipalFromContext(r.Context())
	if !hasRole(p, "admin") {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "admin role required")
		return
	}
	switch {
	case rawPath == "userspaces" && r.Method == http.MethodGet:
		s.handleListUserspaces(w, r)
	case rawPath == "userspaces" && r.Method == http.MethodPost:
		s.handleCreateUserspace(w, r)
	case strings.HasPrefix(rawPath, "userspaces/"):
		s.handleAdminUserspace(w, r, strings.TrimPrefix(rawPath, "userspaces/"))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleCreateUserspace(w http.ResponseWriter, r *http.Request) {
	var req createUserspaceRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "BAD_REQUEST", "invalid json")
		return
	}
	if req.UserID == "" {
		req.UserID = req.UserspaceID
	}
	if err := storage.ValidateUserspaceID(req.UserspaceID); err != nil || req.UserID == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_USERSPACE", "invalid userspace")
		return
	}
	apiKey, err := newAPIKey()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "api key generation failed")
		return
	}
	hash := auth.APIKeyHash(apiKey, s.auth.APIKeyPepper())
	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.store.CreateUserspace(req.UserspaceID, req.UserID, hash); err != nil {
		if errors.Is(err, storage.ErrAlreadyExists) {
			writeError(w, r, http.StatusConflict, "USERSPACE_EXISTS", "userspace already exists")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		return
	}
	s.auth.Invalidate()
	writeJSON(w, http.StatusCreated, createUserspaceResponse{UserID: req.UserID, UserspaceID: req.UserspaceID, APIKey: apiKey})
}

func (s *Server) handleListUserspaces(w http.ResponseWriter, r *http.Request) {
	s.lock.Lock()
	defer s.lock.Unlock()
	infos, err := s.store.ListUserspaces()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		return
	}
	writeJSON(w, http.StatusOK, infos)
}

func (s *Server) handleAdminUserspace(w http.ResponseWriter, r *http.Request, rawPath string) {
	rawUserspaceID, rest, _ := strings.Cut(rawPath, "/")
	userspaceID, err := url.PathUnescape(rawUserspaceID)
	if err != nil || storage.ValidateUserspaceID(userspaceID) != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_USERSPACE", "invalid userspace")
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodDelete:
		s.handleDeleteUserspace(w, r, userspaceID)
	case rest == "api-key" && r.Method == http.MethodPost:
		s.handleRotateUserspaceAPIKey(w, r, userspaceID)
	case rest == "keys" && r.Method == http.MethodGet:
		s.handleListUserspaceKeys(w, r, userspaceID)
	case strings.HasPrefix(rest, "kv/"):
		s.handleAdminKV(w, r, userspaceID, strings.TrimPrefix(rest, "kv/"))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleDeleteUserspace(w http.ResponseWriter, r *http.Request, userspaceID string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.store.DeleteUserspace(userspaceID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "USERSPACE_NOT_FOUND", "userspace not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		return
	}
	s.auth.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRotateUserspaceAPIKey(w http.ResponseWriter, r *http.Request, userspaceID string) {
	apiKey, err := newAPIKey()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "api key generation failed")
		return
	}
	hash := auth.APIKeyHash(apiKey, s.auth.APIKeyPepper())
	s.lock.Lock()
	defer s.lock.Unlock()
	p, err := s.store.ReplaceUserspaceAPIKeyHash(userspaceID, hash)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "USERSPACE_NOT_FOUND", "userspace not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		return
	}
	s.auth.Invalidate()
	writeJSON(w, http.StatusOK, createUserspaceResponse{UserID: p.UserID, UserspaceID: p.UserspaceID, APIKey: apiKey})
}

func (s *Server) handleListUserspaceKeys(w http.ResponseWriter, r *http.Request, userspaceID string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	records, err := s.store.ListUserspaceKeys(userspaceID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "USERSPACE_NOT_FOUND", "userspace not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		return
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *Server) handleAdminKV(w http.ResponseWriter, r *http.Request, userspaceID, rawKey string) {
	key, err := url.PathUnescape(rawKey)
	if err != nil || storage.ValidateKey(key, s.cfg.MaxKeySize) != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_KEY", "invalid key")
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.handlePut(w, r, userspaceID, key)
	case http.MethodGet:
		s.handleGet(w, r, userspaceID, key, false)
	case http.MethodHead:
		s.handleGet(w, r, userspaceID, key, true)
	case http.MethodDelete:
		s.handleDelete(w, r, userspaceID, key)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func hasRole(p model.Principal, role string) bool {
	for _, got := range p.Roles {
		if got == role {
			return true
		}
	}
	return false
}

func newAPIKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
