package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"httpkvdb/internal/auth"
	"httpkvdb/internal/model"
	"httpkvdb/internal/storage"
	httptx "httpkvdb/internal/tx"
)

type createTxRequest struct {
	TxID      string `json:"tx_id"`
	TotalOps  int    `json:"total_ops"`
	TimeoutMS int    `json:"timeout_ms"`
}

func (s *Server) handleCreateTx(w http.ResponseWriter, r *http.Request) {
	var req createTxRequest
	if !readJSONObject(w, r, &req, false) {
		return
	}
	if req.TimeoutMS < 0 || req.TimeoutMS > s.cfg.MaxTxTimeoutMS {
		writeError(w, r, http.StatusBadRequest, "INVALID_TX", "invalid transaction timeout")
		return
	}
	p, _ := auth.PrincipalFromContext(r.Context())
	t, err := s.tx.CreateTx(p, req.TotalOps, time.Duration(req.TimeoutMS)*time.Millisecond, req.TxID)
	if err != nil {
		if errors.Is(err, httptx.ErrInvalidTx) {
			writeError(w, r, http.StatusBadRequest, "INVALID_TX", "invalid transaction")
		} else {
			writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"tx_id": t.TxID, "status": t.Status, "total_ops": t.TotalOps, "deadline": t.Deadline})
}

func (s *Server) handleTx(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}
	txID, err := url.PathUnescape(parts[0])
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TX", "invalid transaction")
		return
	}
	switch {
	case len(parts) == 3 && parts[1] == "ops" && r.Method == http.MethodPost:
		seq, err := strconv.Atoi(parts[2])
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_TX", "invalid seq")
			return
		}
		s.handleAddOp(w, r, txID, seq)
	case len(parts) == 2 && parts[1] == "commit" && r.Method == http.MethodPost:
		s.handleCommit(w, r, txID)
	case len(parts) == 2 && parts[1] == "result" && r.Method == http.MethodGet:
		s.handleTxResult(w, r, txID)
	case len(parts) == 2 && parts[1] == "abort" && r.Method == http.MethodPost:
		s.handleAbort(w, r, txID)
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

func (s *Server) handleAddOp(w http.ResponseWriter, r *http.Request, txID string, seq int) {
	opType := strings.ToUpper(r.Header.Get("X-KV-Op"))
	rawKey := r.Header.Get("X-KV-Key")
	key, err := url.PathUnescape(rawKey)
	if err != nil || storage.ValidateKey(key, s.cfg.MaxKeySize) != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_KEY", "invalid key")
		return
	}
	opID := r.Header.Get("X-KV-Op-Id")
	if opID == "" || !validOp(opType) {
		writeError(w, r, http.StatusBadRequest, "INVALID_TX", "invalid operation")
		return
	}
	var body []byte
	if opType == "PUT" {
		var ok bool
		body, ok = s.readBody(w, r)
		if !ok {
			return
		}
		if len(body) == 0 {
			writeError(w, r, http.StatusBadRequest, "BAD_REQUEST", "PUT fragment requires a non-empty body")
			return
		}
		ct, ok := requestContentType(w, r)
		if !ok {
			return
		}
		if err := storage.ValidateValue(ct, body); err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "INVALID_JSON", "invalid json")
			return
		}
		p, _ := auth.PrincipalFromContext(r.Context())
		status, result, err := s.tx.AddOp(p, txID, model.TxOperation{Seq: seq, OpID: opID, OpType: opType, Key: key, ContentType: ct, Body: body})
		s.writeTxOutcome(w, r, status, result, err, http.StatusAccepted)
		return
	}
	if _, err := readLimitedBody(w, r, 0); err != nil {
		writeError(w, r, http.StatusBadRequest, "BAD_REQUEST", "operation must not include body")
		return
	}
	p, _ := auth.PrincipalFromContext(r.Context())
	status, result, err := s.tx.AddOp(p, txID, model.TxOperation{Seq: seq, OpID: opID, OpType: opType, Key: key})
	s.writeTxOutcome(w, r, status, result, err, http.StatusAccepted)
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request, txID string) {
	var req struct {
		TotalOps int    `json:"total_ops"`
		TxDigest string `json:"tx_digest"`
	}
	if !readJSONObject(w, r, &req, true) {
		return
	}
	p, _ := auth.PrincipalFromContext(r.Context())
	status, result, err := s.tx.Commit(p, txID, req.TotalOps, req.TxDigest)
	if result != nil {
		s.writeTxOutcome(w, r, status, result, err, http.StatusOK)
		return
	}
	s.writeTxOutcome(w, r, status, result, err, http.StatusAccepted)
}

func (s *Server) handleTxResult(w http.ResponseWriter, r *http.Request, txID string) {
	p, _ := auth.PrincipalFromContext(r.Context())
	status, result, err := s.tx.GetResult(p, txID)
	s.writeTxOutcome(w, r, status, result, err, http.StatusOK)
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request, txID string) {
	p, _ := auth.PrincipalFromContext(r.Context())
	status, err := s.tx.Abort(p, txID)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"tx_id": status.TxID, "status": status.Status})
		return
	}
	s.writeTxOutcome(w, r, status, nil, err, http.StatusOK)
}

func (s *Server) writeTxOutcome(w http.ResponseWriter, r *http.Request, status httptx.Status, result *model.TxResult, err error, success int) {
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrTxNotFound):
			writeError(w, r, http.StatusNotFound, "TX_NOT_FOUND", "transaction not found")
		case errors.Is(err, httptx.ErrForbidden):
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "forbidden")
		case errors.Is(err, httptx.ErrSeqConflict):
			writeError(w, r, http.StatusConflict, "SEQ_CONFLICT", "same tx_id and seq received different operation content")
		case errors.Is(err, httptx.ErrAlreadyCommitted):
			writeError(w, r, http.StatusConflict, "TX_ALREADY_COMMITTED", "transaction already committed")
		case errors.Is(err, httptx.ErrAborted):
			writeError(w, r, http.StatusConflict, "TX_ABORTED", "transaction aborted")
		case errors.Is(err, httptx.ErrExpired):
			writeError(w, r, http.StatusGone, "TX_EXPIRED", "transaction expired")
		case errors.Is(err, storage.ErrNotFound):
			writeJSON(w, http.StatusConflict, result)
		case errors.Is(err, httptx.ErrBodyTooLarge):
			writeError(w, r, http.StatusRequestEntityTooLarge, "VALUE_TOO_LARGE", "transaction body limit exceeded")
		case errors.Is(err, httptx.ErrInvalidTx):
			writeError(w, r, http.StatusBadRequest, "INVALID_TX", "invalid transaction")
		default:
			writeError(w, r, http.StatusInternalServerError, "STORAGE_ERROR", "storage error")
		}
		return
	}
	if result != nil {
		writeJSON(w, success, result)
		return
	}
	writeJSON(w, success, status)
}

func validOp(op string) bool {
	switch op {
	case "GET", "PUT", "DELETE", "EXISTS", "HEAD":
		return true
	default:
		return false
	}
}
