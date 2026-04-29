package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"httpkvdb/internal/model"
	"httpkvdb/internal/storage"
)

func TestAPIKeyHashAuthAndCache(t *testing.T) {
	s, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hash := APIKeyHash("secret")
	p := model.Principal{UserID: "u1", UserspaceID: "space1"}
	if err := s.UpsertAPIKeyHash(hash, p); err != nil {
		t.Fatal(err)
	}
	a := New(s, "jwt-secret", "", "", time.Minute, 10)
	got, err := a.Authenticate("ApiKey secret")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u1" || got.UserspaceID != "space1" || got.AuthMethod != "apikey" {
		t.Fatalf("bad principal: %+v", got)
	}
	if _, err := a.Authenticate("ApiKey wrong"); err != ErrUnauthorized {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestJWTSubjectMapping(t *testing.T) {
	s, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertJWTSubject("sub1", model.Principal{UserID: "u1", UserspaceID: "space1"}); err != nil {
		t.Fatal(err)
	}
	a := New(s, "jwt-secret", "issuer", "aud", time.Minute, 10)
	token := signTestJWT(t, "jwt-secret", map[string]any{"sub": "sub1", "iss": "issuer", "aud": "aud", "exp": time.Now().Add(time.Hour).Unix()})
	got, err := a.Authenticate("Bearer " + token)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u1" || got.UserspaceID != "space1" || got.AuthMethod != "jwt" {
		t.Fatalf("bad principal: %+v", got)
	}
}

func signTestJWT(t *testing.T, secret string, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "HS256", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	h := base64.RawURLEncoding.EncodeToString(header)
	p := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(h + "." + p))
	return h + "." + p + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
