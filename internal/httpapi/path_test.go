package httpapi

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"httpkvdb/internal/auth"
	"httpkvdb/internal/importexport"
	"httpkvdb/internal/model"
)

func TestKVPathsDecodeExactlyOnceWithoutRedirects(t *testing.T) {
	h, store := newTestHTTPStore(t, t.TempDir())
	prefixes := []string{"/v1/kv/", "/api/v1/space-a/", "/v1/admin/userspaces/space-a/kv/"}
	keys := []string{"100%", "a/b", "a%2Fb", "+", "a?b#c", "中文/路径", ".", "..", "a//b", "a/../b", "/leading/", strings.Repeat("x", 4096)}
	for i, key := range keys {
		value := []byte(fmt.Sprintf("record-%d", i))
		for _, prefix := range prefixes {
			credential := "key-a"
			if strings.Contains(prefix, "/admin/") {
				credential = "key-admin"
			}
			target := prefix + url.PathEscape(key)
			do(t, h, "PUT", target, credential, "text/plain", value, 200)
			got, err := store.Get("space-a", key)
			if err != nil || !bytes.Equal(got.Value, value) {
				t.Fatalf("key resolved incorrectly for %s: %v", prefix, err)
			}
			if got := do(t, h, "GET", target, credential, "", nil, 200); !bytes.Equal(got, value) {
				t.Fatal("round trip mismatch")
			}
			do(t, h, "HEAD", target, credential, "", nil, 200)
		}
	}
	// Raw slashes and dot segments in the remainder also belong to the key.
	do(t, h, "PUT", "/v1/kv/a//../b", "key-a", "text/plain", []byte("raw"), 200)
	if _, err := store.Get("space-a", "a//../b"); err != nil {
		t.Fatal("router normalized key", err)
	}
}

func TestReservedUserspaceNamesRemainAccessible(t *testing.T) {
	h, store := newTestHTTPStore(t, t.TempDir())
	for _, space := range []string{"kv", "tx", "admin", "export", "import"} {
		key := "key-" + space
		if err := store.UpsertAPIKeyHash(auth.APIKeyHash(key, "pepper"), model.Principal{UserID: space, UserspaceID: space}); err != nil {
			t.Fatal(err)
		}
		do(t, h, "PUT", "/api/v1/"+space+"/profile", key, "text/plain", []byte(space), 200)
		if got := do(t, h, "GET", "/api/v1/"+space+"/profile", key, "", nil, 200); string(got) != space {
			t.Fatal("reserved space misrouted")
		}
	}
}

func TestTransactionHeaderKeyMatchesCRUDPath(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	for i, key := range []string{"100%", "a%2Fb", "a/b", "中文/路径"} {
		id := fmt.Sprintf("encoded-%d", i)
		do(t, h, "POST", "/v1/tx", "key-a", "application/json", []byte(fmt.Sprintf(`{"tx_id":%q,"total_ops":1,"timeout_ms":10000}`, id)), 201)
		req := httptest.NewRequest(http.MethodPost, "/v1/tx/"+id+"/ops/1", strings.NewReader("value"))
		req.Header.Set("APIKey", "key-a")
		req.Header.Set("X-KV-Op", "PUT")
		req.Header.Set("X-KV-Key", url.PathEscape(key))
		req.Header.Set("X-KV-Op-Id", "op")
		req.Header.Set("Content-Type", "text/plain")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 202 {
			t.Fatalf("add op = %d", w.Code)
		}
		do(t, h, "POST", "/v1/tx/"+id+"/commit", "key-a", "application/json", []byte(`{}`), 200)
		if got := do(t, h, "GET", "/v1/kv/"+url.PathEscape(key), "key-a", "", nil, 200); string(got) != "value" {
			t.Fatal("transaction key differs from path key")
		}
	}
}

func TestSlowGETResponseDoesNotHoldGlobalLock(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	do(t, h, "PUT", "/v1/kv/slow", "key-a", "text/plain", []byte("value"), 200)
	done := make(chan struct{})
	w := &blockedWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), release: make(chan struct{})}
	defer func() { close(w.release); <-done }()
	req := httptest.NewRequest("GET", "/v1/kv/slow", nil)
	req.Header.Set("APIKey", "key-a")
	go func() { h.ServeHTTP(w, req); close(done) }()
	<-w.started
	updated := make(chan int, 1)
	go func() {
		req := httptest.NewRequest("PUT", "/v1/kv/other", strings.NewReader("new"))
		req.Header.Set("APIKey", "key-a")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		updated <- rr.Code
	}()
	select {
	case status := <-updated:
		if status != 200 {
			t.Fatalf("PUT status=%d", status)
		}
	case <-time.After(2 * time.Second):
		t.Error("response body blocked unrelated writes")
	}
}

type blockedWriter struct {
	*httptest.ResponseRecorder
	started, release chan struct{}
}

func (w *blockedWriter) Write(p []byte) (int, error) {
	close(w.started)
	<-w.release
	return w.ResponseRecorder.Write(p)
}

func TestImportEnforcesTotalAndPerValueLimitsAtomically(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	do(t, h, "PUT", "/v1/kv/kept", "key-a", "text/plain", []byte("old"), 200)
	// The total import limit is 4 MiB for this test server.
	do(t, h, "POST", "/v1/import", "key-a", "application/octet-stream", bytes.Repeat([]byte("x"), (4<<20)+1), 413)
	records := []model.KVRecord{{Key: "too-large", Value: bytes.Repeat([]byte("x"), (1<<20)+1), ContentType: "text/plain", CreatedAt: time.Now(), UpdatedAt: time.Now()}}
	encoded, err := importexport.Encode(records)
	if err != nil {
		t.Fatal(err)
	}
	do(t, h, "POST", "/v1/import", "key-a", "application/octet-stream", encoded, 400)
	if got := do(t, h, "GET", "/v1/kv/kept", "key-a", "", nil, 200); string(got) != "old" {
		t.Fatal("rejected import changed existing data")
	}
}

func TestEncodedTransactionIDAndInvalidCommitSuffix(t *testing.T) {
	h := newTestHTTP(t, t.TempDir())
	do(t, h, "POST", "/v1/tx", "key-a", "application/json", []byte(`{"tx_id":"tx:encoded","total_ops":1}`), 201)
	do(t, h, "GET", "/v1/tx/tx%3Aencoded/result", "key-a", "", nil, 200)
	for _, body := range []string{`{} {}`, `{} garbage`, `{"total_ops":"invalid"}`} {
		do(t, h, "POST", "/v1/tx/tx%3Aencoded/commit", "key-a", "application/json", []byte(body), 400)
	}
}

func TestClosedStorageReturnsServerErrorForTransactionAPIs(t *testing.T) {
	h, store := newTestHTTPStore(t, t.TempDir())
	// Populate the auth cache before closing the underlying database.
	do(t, h, "GET", "/v1/kv/absent", "key-a", "", nil, 404)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	do(t, h, "POST", "/v1/tx", "key-a", "application/json", []byte(`{"total_ops":1}`), 500)
	do(t, h, "POST", "/v1/tx/absent/commit", "key-a", "application/json", []byte(`{}`), 500)
	do(t, h, "GET", "/readyz", "", "", nil, 503)
}
