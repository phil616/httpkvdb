package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"httpkvdb/internal/storage"
)

const maxJSONRequestSize int64 = 1 << 20

func readLimitedBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	return io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
}

func writeBodyError(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "VALUE_TOO_LARGE", "request body exceeds limit")
	} else {
		writeError(w, r, http.StatusBadRequest, "BAD_REQUEST", "could not read request body")
	}
}

// JSON control endpoints accept one object, never a prefix of a malformed body.
// An empty commit body remains supported for compatibility.
func readJSONObject(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) bool {
	body, err := readLimitedBody(w, r, maxJSONRequestSize)
	if err != nil {
		writeBodyError(w, r, err)
		return false
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 && allowEmpty {
		return true
	}
	if len(body) == 0 || body[0] != '{' || json.Unmarshal(body, dst) != nil {
		writeError(w, r, http.StatusBadRequest, "BAD_REQUEST", "expected one JSON object")
		return false
	}
	return true
}

func requestContentType(w http.ResponseWriter, r *http.Request) (string, bool) {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	if storage.ValidateContentType(ct) != nil {
		writeError(w, r, http.StatusBadRequest, "BAD_REQUEST", "invalid content type")
		return "", false
	}
	return ct, true
}
