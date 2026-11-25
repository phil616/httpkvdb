package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"httpkvdb/internal/auth"
	"httpkvdb/internal/config"
	"httpkvdb/internal/importexport"
	"httpkvdb/internal/lock"
	"httpkvdb/internal/model"
	"httpkvdb/internal/observe"
	"httpkvdb/internal/storage"
	httptx "httpkvdb/internal/tx"
)

func newTestHTTP(t *testing.T, dir string) http.Handler {
	h, _ := newTestHTTPStore(t, dir)
	return h
}

func newTestHTTPStore(t *testing.T, dir string, configure ...func(*config.Config)) (http.Handler, *storage.Store) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.StoragePath = dir
	cfg.MaxValueSize = 1 << 20
	cfg.MaxImportSize = 4 << 20
	cfg.MaxKeySize = 4096
	cfg.MaxTxOps = 1000
	cfg.DefaultTxTimeoutMS = 1000
	cfg.MaxTxTimeoutMS = 10000
	for _, apply := range configure {
		apply(&cfg)
	}
	s, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.UpsertAPIKeyHash(auth.APIKeyHash("key-a", "pepper"), model.Principal{UserID: "a", UserspaceID: "space-a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAPIKeyHash(auth.APIKeyHash("key-b", "pepper"), model.Principal{UserID: "b", UserspaceID: "space-b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAPIKeyHash(auth.APIKeyHash("key-alice", "pepper"), model.Principal{UserID: "alice", UserspaceID: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAPIKeyHash(auth.APIKeyHash("key-admin", "pepper"), model.Principal{UserID: "admin", UserspaceID: "admin", Roles: []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	serial := &lock.Serializable{}
	authn := auth.New(s, "jwt-secret", "pepper", "", "", time.Minute, 100)
	metrics := &observe.Metrics{}
	coord := httptx.NewCoordinator(s, serial, cfg.MaxTxOps, time.Second, 10*time.Second, httptx.Options{MaxBodySize: cfg.MaxTxBodySize, ResultRetention: time.Duration(cfg.TxResultRetentionMS) * time.Millisecond, Metrics: metrics})
	return NewServer(cfg, s, authn, serial, coord, metrics).Handler(), s
}

func TestHTTPPutGetDeleteAndIsolation(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	do(t, h, http.MethodPut, "/v1/kv/same", "key-a", "text/plain", []byte("one"), http.StatusOK)
	do(t, h, http.MethodPut, "/v1/kv/same", "key-b", "text/plain", []byte("two"), http.StatusOK)
	resA := do(t, h, http.MethodGet, "/v1/kv/same", "key-a", "", nil, http.StatusOK)
	if string(resA) != "one" {
		t.Fatalf("user a got %q", string(resA))
	}
	resB := do(t, h, http.MethodGet, "/v1/kv/same", "key-b", "", nil, http.StatusOK)
	if string(resB) != "two" {
		t.Fatalf("user b got %q", string(resB))
	}
	do(t, h, http.MethodDelete, "/v1/kv/same", "key-a", "", nil, http.StatusNoContent)
	do(t, h, http.MethodGet, "/v1/kv/same", "key-a", "", nil, http.StatusNotFound)
	do(t, h, http.MethodGet, "/v1/kv/same", "key-b", "", nil, http.StatusOK)
}

func TestHTTPInvalidJSONAndBinary(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	do(t, h, http.MethodPut, "/v1/kv/json", "key-a", "application/json", []byte(`{"bad"`), http.StatusUnprocessableEntity)
	payload := []byte{0, 1, 2, 3, 255}
	do(t, h, http.MethodPut, "/v1/kv/bin", "key-a", "application/octet-stream", payload, http.StatusOK)
	got := do(t, h, http.MethodGet, "/v1/kv/bin", "key-a", "", nil, http.StatusOK)
	if !bytes.Equal(got, payload) {
		t.Fatalf("binary mismatch: %v", got)
	}
}

func TestHTTPUserspacePathAPIKeyHeaderAndContentTypes(t *testing.T) {
	dir := t.TempDir()
	h := newTestHTTP(t, dir)
	doWithAPIKeyHeader(t, h, http.MethodPut, "/api/v1/alice/profile", "key-alice", "text/plain", []byte("Alice"), http.StatusOK)
	if got := doWithAPIKeyHeader(t, h, http.MethodGet, "/api/v1/alice/profile", "key-alice", "", nil, http.StatusOK); string(got) != "Alice" {
		t.Fatalf("userspace path get mismatch: %q", string(got))
	}
	doWithAPIKeyHeader(t, h, http.MethodGet, "/api/v1/space-b/profile", "key-alice", "", nil, http.StatusForbidden)

	doWithAPIKeyHeader(t, h, http.MethodPut, "/api/v1/alice/profile", "key-alice", "application/json", []byte(`{"name":"Alice"}`), http.StatusOK)
	if got := do(t, h, http.MethodGet, "/api/v1/alice/profile", "key-alice", "", nil, http.StatusOK); string(got) != `{"name":"Alice"}` {
		t.Fatal("JSON overwrite mismatch")
	}

	doWithAPIKeyHeader(t, h, http.MethodPut, "/api/v1/alice/avatar", "key-alice", "application/octet-stream", []byte{0, 1, 2}, http.StatusOK)
	if got := do(t, h, http.MethodGet, "/api/v1/alice/avatar", "key-alice", "", nil, http.StatusOK); !bytes.Equal(got, []byte{0, 1, 2}) {
		t.Fatal("binary mismatch")
	}

	doWithAPIKeyHeader(t, h, http.MethodPut, "/api/v1/alice/mixed", "key-alice", "image/png", []byte{3, 4}, http.StatusOK)
	if got := do(t, h, http.MethodGet, "/api/v1/alice/mixed", "key-alice", "", nil, http.StatusOK); !bytes.Equal(got, []byte{3, 4}) {
		t.Fatal("custom content type mismatch")
	}
}

func TestHTTPAdminCreateUserspace(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	body := []byte(`{"userspace_id":"bob","user_id":"bob"}`)
	res := do(t, h, http.MethodPost, "/v1/admin/userspaces", "key-admin", "application/json", body, http.StatusCreated)
	var out struct {
		UserID      string `json:"user_id"`
		UserspaceID string `json:"userspace_id"`
		APIKey      string `json:"api_key"`
	}
	if err := json.NewDecoder(bytes.NewReader(res)).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.UserID != "bob" || out.UserspaceID != "bob" || out.APIKey == "" {
		t.Fatalf("unexpected create userspace response: %+v", out)
	}
	doWithAPIKeyHeader(t, h, http.MethodPut, "/api/v1/bob/profile", out.APIKey, "text/plain", []byte("Bob"), http.StatusOK)
	if got := doWithAPIKeyHeader(t, h, http.MethodGet, "/api/v1/bob/profile", out.APIKey, "", nil, http.StatusOK); string(got) != "Bob" {
		t.Fatalf("created userspace get mismatch: %q", string(got))
	}
	do(t, h, http.MethodPost, "/v1/admin/userspaces", "key-admin", "application/json", body, http.StatusConflict)
	do(t, h, http.MethodPost, "/v1/admin/userspaces", "key-a", "application/json", []byte(`{"userspace_id":"eve"}`), http.StatusForbidden)
}

func TestHTTPAdminUserspaceAndKVManagement(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	do(t, h, http.MethodGet, "/v1/admin/userspaces", "key-a", "", nil, http.StatusForbidden)
	res := do(t, h, http.MethodPost, "/v1/admin/userspaces", "key-admin", "application/json", []byte(`{"userspace_id":"managed","user_id":"managed-user"}`), http.StatusCreated)
	var created struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(bytes.NewReader(res)).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.APIKey == "" {
		t.Fatal("missing generated api key")
	}
	do(t, h, http.MethodPut, "/v1/admin/userspaces/managed/kv/profile", "key-admin", "text/plain", []byte("admin-write"), http.StatusOK)
	if got := do(t, h, http.MethodGet, "/api/v1/managed/profile", created.APIKey, "", nil, http.StatusOK); string(got) != "admin-write" {
		t.Fatalf("managed userspace read mismatch: %q", string(got))
	}
	keys := do(t, h, http.MethodGet, "/v1/admin/userspaces/managed/keys", "key-admin", "", nil, http.StatusOK)
	if !strings.Contains(string(keys), `"key":"profile"`) || strings.Contains(string(keys), "admin-write") {
		t.Fatalf("unexpected key listing: %s", string(keys))
	}
	listed := do(t, h, http.MethodGet, "/v1/admin/userspaces", "key-admin", "", nil, http.StatusOK)
	if !strings.Contains(string(listed), `"userspace_id":"managed"`) {
		t.Fatalf("userspace not listed: %s", string(listed))
	}
	rotated := do(t, h, http.MethodPost, "/v1/admin/userspaces/managed/api-key", "key-admin", "application/json", nil, http.StatusOK)
	var out struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(bytes.NewReader(rotated)).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.APIKey == "" || out.APIKey == created.APIKey {
		t.Fatalf("invalid rotated api key")
	}
	do(t, h, http.MethodGet, "/api/v1/managed/profile", created.APIKey, "", nil, http.StatusUnauthorized)
	do(t, h, http.MethodGet, "/api/v1/managed/profile", out.APIKey, "", nil, http.StatusOK)
	do(t, h, http.MethodDelete, "/v1/admin/userspaces/managed/kv/profile", "key-admin", "", nil, http.StatusNoContent)
	do(t, h, http.MethodGet, "/api/v1/managed/profile", out.APIKey, "", nil, http.StatusNotFound)
	do(t, h, http.MethodDelete, "/v1/admin/userspaces/managed", "key-admin", "", nil, http.StatusNoContent)
	do(t, h, http.MethodGet, "/api/v1/managed/profile", out.APIKey, "", nil, http.StatusUnauthorized)
}

func TestHTTPExportImportModes(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	do(t, h, http.MethodPut, "/v1/kv/a", "key-a", "text/plain", []byte("one"), http.StatusOK)
	exported := do(t, h, http.MethodGet, "/v1/export", "key-a", "", nil, http.StatusOK)
	do(t, h, http.MethodPost, "/v1/import", "key-b", "application/octet-stream", exported, http.StatusOK)
	if got := do(t, h, http.MethodGet, "/v1/kv/a", "key-b", "", nil, http.StatusOK); string(got) != "one" {
		t.Fatalf("import replace mismatch: %q", string(got))
	}
	records, err := importexport.Decode(exported, 4096, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	records[0].Value = []byte("new")
	overwrite, err := importexport.Encode(records)
	if err != nil {
		t.Fatal(err)
	}
	doWithMode(t, h, "/v1/import", "key-b", "merge-skip", overwrite, http.StatusOK)
	if got := do(t, h, http.MethodGet, "/v1/kv/a", "key-b", "", nil, http.StatusOK); string(got) != "one" {
		t.Fatalf("merge-skip overwrote value: %q", string(got))
	}
	doWithMode(t, h, "/v1/import", "key-b", "merge-overwrite", overwrite, http.StatusOK)
	if got := do(t, h, http.MethodGet, "/v1/kv/a", "key-b", "", nil, http.StatusOK); string(got) != "new" {
		t.Fatalf("merge-overwrite did not overwrite: %q", string(got))
	}
}

func TestHTTPPersistenceAfterRestart(t *testing.T) {
	dir := t.TempDir()
	h, store := newTestHTTPStore(t, dir)
	do(t, h, http.MethodPut, "/v1/kv/persist", "key-a", "text/plain", []byte("kept"), http.StatusOK)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	h = newTestHTTP(t, dir)
	if got := do(t, h, http.MethodGet, "/v1/kv/persist", "key-a", "", nil, http.StatusOK); string(got) != "kept" {
		t.Fatalf("persisted value mismatch: %q", string(got))
	}
}

func TestHTTPTransactionOpNotVisibleBeforeCommit(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	reqBody := []byte(`{"tx_id":"tx-http","total_ops":1,"timeout_ms":1000}`)
	do(t, h, http.MethodPost, "/v1/tx", "key-a", "application/json", reqBody, http.StatusCreated)
	req := httptest.NewRequest(http.MethodPost, "/v1/tx/tx-http/ops/1", bytes.NewReader([]byte("hidden")))
	req.Header.Set("Authorization", "ApiKey key-a")
	req.Header.Set("X-KV-Op", "PUT")
	req.Header.Set("X-KV-Key", "txkey")
	req.Header.Set("X-KV-Op-Id", "op1")
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("add op status=%d body=%s", rr.Code, rr.Body.String())
	}
	do(t, h, http.MethodGet, "/v1/kv/txkey", "key-a", "", nil, http.StatusNotFound)
	do(t, h, http.MethodPost, "/v1/tx/tx-http/commit", "key-a", "application/json", []byte(`{"total_ops":1}`), http.StatusOK)
	if got := do(t, h, http.MethodGet, "/v1/kv/txkey", "key-a", "", nil, http.StatusOK); string(got) != "hidden" {
		t.Fatalf("committed value mismatch: %q", string(got))
	}
}

func TestHTTPConcurrentWritesSerializable(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			do(t, h, http.MethodPut, "/v1/kv/concurrent", "key-a", "text/plain", []byte(strconv.Itoa(i)), http.StatusOK)
		}(i)
	}
	wg.Wait()
	got := string(do(t, h, http.MethodGet, "/v1/kv/concurrent", "key-a", "", nil, http.StatusOK))
	v, err := strconv.Atoi(got)
	if err != nil || v < 0 || v >= n {
		t.Fatalf("final value is not from a serial write: %q", got)
	}
}

func TestCORSPreflightAllowsViteDevOriginWithoutAuth(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	req := httptest.NewRequest(http.MethodOptions, "/v1/kv/profile", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, APIKey, X-API-Key, Content-Type, X-KV-Op")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("preflight got status %d want 204 body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5173" {
		t.Fatalf("allow origin = %q", got)
	}
	if got := rr.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Authorization") || !strings.Contains(got, "APIKey") || !strings.Contains(got, "X-API-Key") || !strings.Contains(got, "X-KV-Op") {
		t.Fatalf("allow headers missing expected values: %q", got)
	}
	if got := rr.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-KV-Version") || !strings.Contains(got, "X-KV-Checksum") {
		t.Fatalf("expose headers missing expected values: %q", got)
	}
}

func do(t *testing.T, h http.Handler, method, path, key, ct string, body []byte, want int) []byte {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "ApiKey "+key)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("%s %s got status %d want %d body=%s", method, path, rr.Code, want, rr.Body.String())
	}
	out, _ := io.ReadAll(rr.Result().Body)
	return out
}

func doWithMode(t *testing.T, h http.Handler, path, key, mode string, body []byte, want int) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "ApiKey "+key)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-KV-Import-Mode", mode)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("import got status %d want %d body=%s", rr.Code, want, rr.Body.String())
	}
	return rr.Body.Bytes()
}

func doWithAPIKeyHeader(t *testing.T, h http.Handler, method, path, key, ct string, body []byte, want int) []byte {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("APIKey", key)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("%s %s got status %d want %d body=%s", method, path, rr.Code, want, rr.Body.String())
	}
	return rr.Body.Bytes()
}
