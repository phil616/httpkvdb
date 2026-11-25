package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"httpkvdb/internal/model"
)

func TestJWTRequiresHS256HeaderAndExpiry(t *testing.T) {
	a := New(nil, "secret", "pepper", "", "", time.Minute, 10)
	future := time.Now().Add(time.Hour).Unix()
	for _, test := range []struct {
		header, payload string
		valid           bool
	}{
		{`{"alg":"HS256"}`, fmt.Sprintf(`{"sub":"u","exp":%d}`, future), true},
		{`{"alg":"none"}`, fmt.Sprintf(`{"sub":"u","exp":%d}`, future), false},
		{`{"alg":"HS512"}`, fmt.Sprintf(`{"sub":"u","exp":%d}`, future), false},
		{`{"alg":"HS256"}`, `{"sub":"u"}`, false},
		{`{"alg":"HS256"}`, `{"sub":"u","exp":1}`, false},
		{`{"alg":"HS256","crit":["extension"]}`, fmt.Sprintf(`{"sub":"u","exp":%d}`, future), false},
	} {
		signed := base64.RawURLEncoding.EncodeToString([]byte(test.header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(test.payload))
		mac := hmac.New(sha256.New, []byte("secret"))
		_, _ = mac.Write([]byte(signed))
		token := signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		_, err := a.verifyJWT(token)
		if (err == nil) != test.valid {
			t.Fatalf("JWT validity mismatch for header %s", test.header)
		}
	}
}

func TestAuthenticationRejectsConflictingOrRepeatedHeaders(t *testing.T) {
	a := New(nil, "secret", "pepper", "", "", time.Minute, 10)
	for _, names := range [][]string{{"APIKey", "X-API-Key"}, {"APIKey", "Authorization"}, {"APIKey", "APIKey"}, {"Authorization", "Authorization"}} {
		r := httptest.NewRequest("GET", "/", nil)
		for _, name := range names {
			r.Header.Add(name, "credential")
		}
		if _, err := a.AuthenticateRequest(r); err != ErrUnauthorized {
			t.Fatal("ambiguous credentials accepted")
		}
	}
}

type delayedAuthStore struct {
	started, release chan struct{}
	once             sync.Once
}

func (s *delayedAuthStore) ResolveAPIKeyHash(string) (model.Principal, bool) {
	stale := false
	s.once.Do(func() { stale = true; close(s.started); <-s.release })
	return model.Principal{UserID: "revoked", UserspaceID: "u"}, stale
}
func (s *delayedAuthStore) ResolveJWTSubject(string) (model.Principal, bool) {
	return model.Principal{}, false
}
func TestInvalidationCannotBeUndoneByInflightCacheFill(t *testing.T) {
	store := &delayedAuthStore{started: make(chan struct{}), release: make(chan struct{})}
	a := New(store, "secret", "pepper", "", "", time.Minute, 10)
	done := make(chan error, 1)
	go func() { _, err := a.Authenticate("ApiKey revoked"); done <- err }()
	<-store.started
	a.Invalidate()
	close(store.release)
	if err := <-done; err != ErrUnauthorized {
		t.Fatal("revoked lookup survived invalidation")
	}
	if _, err := a.Authenticate("ApiKey revoked"); err != ErrUnauthorized {
		t.Fatal("revoked principal cached")
	}
}
