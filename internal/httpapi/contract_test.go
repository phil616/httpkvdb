package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"httpkvdb/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONEndpointsRejectTrailingContentAndWrongShapes(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	for _, endpoint := range []string{"/v1/tx", "/v1/admin/userspaces"} {
		for _, body := range []string{`{}`, `null`, `[]`, `{"total_ops":1,"userspace_id":"x"} {}`, `{"total_ops":1,"userspace_id":"x"} trailing`} {
			if body == `{}` {
				continue
			}
			do(t, h, "POST", endpoint, "key-admin", "application/json", []byte(body), 400)
		}
		do(t, h, "POST", endpoint, "key-admin", "application/json", bytes.Repeat([]byte(" "), (1<<20)+1), 413)
	}
	for _, timeout := range []string{"-1", "9223372036854775807"} {
		do(t, h, "POST", "/v1/tx", "key-a", "application/json", []byte(`{"total_ops":1,"timeout_ms":`+timeout+`}`), 400)
	}
}

func TestNonPutFragmentRejectsUnknownLengthBody(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	do(t, h, "POST", "/v1/tx", "key-a", "application/json", []byte(`{"tx_id":"chunked","total_ops":1}`), 201)
	for _, op := range []string{"GET", "HEAD", "EXISTS", "DELETE", "PUT"} {
		body := "unexpected"
		if op == "PUT" {
			body = ""
		}
		req := httptest.NewRequest("POST", "/v1/tx/chunked/ops/1", strings.NewReader(body))
		req.ContentLength = -1
		req.TransferEncoding = []string{"chunked"}
		req.Header.Set("APIKey", "key-a")
		req.Header.Set("X-KV-Op", op)
		req.Header.Set("X-KV-Key", "key")
		req.Header.Set("X-KV-Op-Id", "op")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("%s accepted forbidden body: %d", op, w.Code)
		}
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("transport failed") }
func TestBodyReadFailureIsNotMisreportedAsOversizedValue(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	req := httptest.NewRequest("PUT", "/v1/kv/a", nil)
	req.Body = io.NopCloser(failedReader{})
	req.Header.Set("APIKey", "key-a")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "BAD_REQUEST") {
		t.Fatal("read failure not classified correctly")
	}
}

func TestRoutingErrorsUseJSONAndHeadHasNoBody(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	for _, test := range []struct {
		method, path, credential string
		status                   int
	}{
		{"GET", "/missing", "", 404}, {"GET", "/v1/missing", "key-a", 404}, {"POST", "/v1/kv/a", "key-a", 405},
		{"HEAD", "/v1/kv/a", "", 401}, {"HEAD", "/v1/kv/a", "key-a", 404}, {"HEAD", "/healthz", "", 200}, {"HEAD", "/readyz", "", 200},
	} {
		req := httptest.NewRequest(test.method, test.path, nil)
		if test.credential != "" {
			req.Header.Set("APIKey", test.credential)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != test.status {
			t.Fatalf("unexpected %s status %d", test.path, w.Code)
		}
		if test.method == http.MethodHead {
			if w.Body.Len() != 0 {
				t.Fatal("HEAD included a body")
			}
			continue
		}
		var response ErrorResponse
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Error == "" || response.RequestID == "" {
			t.Fatal("routing error has invalid JSON contract")
		}
	}
}

func TestWriteRejectsInvalidContentTypeMetadata(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	for _, contentType := range []string{strings.Repeat("a", 65536), "text/plain\r\nInjected: true", "text/plain\x00"} {
		do(t, h, "PUT", "/v1/kv/key", "key-a", contentType, []byte("value"), 400)
	}
	do(t, h, "GET", "/v1/kv/key", "key-a", "", nil, 404)
}

func TestHTTPTransactionAggregateLimit(t *testing.T) {
	h, _ := newTestHTTPStore(t, t.TempDir(), func(cfg *config.Config) { cfg.MaxTxBodySize = 6 })
	do(t, h, "POST", "/v1/tx", "key-a", "application/json", []byte(`{"tx_id":"limit-http","total_ops":2}`), 201)
	for i, body := range []string{"123", "4567"} {
		seq := []string{"1", "2"}[i]
		req := httptest.NewRequest("POST", "/v1/tx/limit-http/ops/"+seq, strings.NewReader(body))
		req.Header.Set("APIKey", "key-a")
		req.Header.Set("X-KV-Op", "PUT")
		req.Header.Set("X-KV-Key", "key"+seq)
		req.Header.Set("X-KV-Op-Id", "op"+seq)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		want := 202
		if i == 1 {
			want = 413
		}
		if w.Code != want {
			t.Fatalf("fragment status=%d, want=%d", w.Code, want)
		}
	}
	status := do(t, h, "GET", "/v1/tx/limit-http/result", "key-a", "", nil, 200)
	if !bytes.Contains(status, []byte(`"received_seq":[1]`)) {
		t.Fatal("rejected fragment was received")
	}
}
