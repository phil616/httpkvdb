package httpapi

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"

	"httpkvdb/internal/auth"
	"httpkvdb/internal/importexport"
	"httpkvdb/internal/model"
	"httpkvdb/internal/storage"
)

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.PrincipalFromContext(r.Context())
	s.lock.Lock()
	defer s.lock.Unlock()
	records, err := s.store.ExportUserspace(p.UserspaceID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		return
	}
	data, err := importexport.Encode(records)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "export error")
		return
	}
	atomic.AddUint64(&s.metrics.Export, 1)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="kv-export.bin"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	mode := model.ImportMode(r.Header.Get("X-KV-Import-Mode"))
	if mode == "" {
		mode = model.ImportReplace
	}
	if mode != model.ImportReplace && mode != model.ImportMergeOverwrite && mode != model.ImportMergeSkip {
		writeError(w, r, http.StatusBadRequest, "BAD_REQUEST", "invalid import mode")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxValueSize))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "VALUE_TOO_LARGE", "import too large")
		return
	}
	records, err := importexport.Decode(data, s.cfg.MaxKeySize, s.cfg.MaxValueSize)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidJSON) {
			writeError(w, r, http.StatusUnprocessableEntity, "INVALID_JSON", "invalid json")
			return
		}
		writeError(w, r, http.StatusBadRequest, "BAD_REQUEST", "invalid import")
		return
	}
	p, _ := auth.PrincipalFromContext(r.Context())
	s.lock.Lock()
	defer s.lock.Unlock()
	res, err := s.store.ImportUserspace(p.UserspaceID, records, mode)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		return
	}
	atomic.AddUint64(&s.metrics.Import, 1)
	writeJSON(w, http.StatusOK, res)
}
