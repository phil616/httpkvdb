package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPercentKeyRoundTrip(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	do(t, h, "PUT", "/v1/kv/100%25", "key-a", "text/plain", []byte("x"), 200)
	if got := do(t, h, "GET", "/v1/kv/100%25", "key-a", "", nil, 200); string(got) != "x" {
		t.Fatal("percent key mismatch")
	}
}
func TestEncodedAndLiteralSlashesAreDistinctKeys(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	do(t, h, "PUT", "/v1/kv/a%252Fb", "key-a", "text/plain", []byte("literal"), 200)
	do(t, h, "GET", "/v1/kv/a%2Fb", "key-a", "", nil, 404)
	do(t, h, "PUT", "/v1/kv/a%2Fb", "key-a", "text/plain", []byte("slash"), 200)
	if got := do(t, h, "GET", "/v1/kv/a%252Fb", "key-a", "", nil, 200); string(got) != "literal" {
		t.Fatal("keys aliased")
	}
}
func TestUserspaceRouteAlwaysChecksPrincipal(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	for _, space := range []string{"kv", "tx", "admin", "export", "import"} {
		do(t, h, "PUT", "/api/v1/"+space+"/profile", "key-a", "text/plain", []byte("x"), 403)
	}
	do(t, h, "GET", "/v1/kv/profile", "key-a", "", nil, 404)
}
func TestExportCanReimportMultipleValues(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	body := bytes.Repeat([]byte("x"), 600000)
	for _, k := range []string{"a", "b"} {
		do(t, h, "PUT", "/v1/kv/"+k, "key-a", "text/plain", body, 200)
	}
	data := do(t, h, "GET", "/v1/export", "key-a", "", nil, 200)
	do(t, h, "POST", "/v1/import", "key-b", "application/octet-stream", data, 200)
	for _, key := range []string{"a", "b"} {
		if got := do(t, h, "GET", "/v1/kv/"+key, "key-b", "", nil, 200); !bytes.Equal(got, body) {
			t.Fatal("imported value mismatch")
		}
	}
}
func TestMalformedCommitDoesNotExecute(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	do(t, h, "POST", "/v1/tx", "key-a", "application/json", []byte(`{"tx_id":"badjson","total_ops":1}`), 201)
	r := httptest.NewRequest("POST", "/v1/tx/badjson/ops/1", nil)
	r.Header.Set("APIKey", "key-a")
	r.Header.Set("X-KV-Op", "EXISTS")
	r.Header.Set("X-KV-Key", "k")
	r.Header.Set("X-KV-Op-Id", "op")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatal(w.Code)
	}
	v := do(t, h, "POST", "/v1/tx/badjson/commit", "key-a", "application/json", []byte(`{`), 400)
	if !bytes.Contains(v, []byte("BAD_REQUEST")) {
		t.Fatal("expected bad request")
	}
	result := do(t, h, "GET", "/v1/tx/badjson/result", "key-a", "", nil, 200)
	if bytes.Contains(result, []byte("committed")) {
		t.Fatal("malformed commit executed")
	}
}
func TestRequestIDCannotInjectJSON(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	r := httptest.NewRequest(http.MethodGet, "/v1/kv/k", nil)
	r.Header.Set("X-Request-Id", `x","injected":true,"pad":"`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var v map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
		t.Fatal(e)
	}
	if _, exists := v["injected"]; exists {
		t.Fatal("request ID injected JSON field")
	}
	if v["request_id"] != r.Header.Get("X-Request-Id") {
		t.Fatal("request ID was not preserved")
	}
}
