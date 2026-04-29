package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"httpkvdb/internal/model"
)

var ErrUnauthorized = errors.New("unauthorized")

type Store interface {
	ResolveAPIKeyHash(hash string) (model.Principal, bool)
	ResolveJWTSubject(subject string) (model.Principal, bool)
}

type Authenticator struct {
	store        Store
	jwtSecret    string
	apiKeyPepper string
	issuer       string
	audience     string
	ttl          time.Duration
	max          int
	mu           sync.Mutex
	cache        map[string]cacheEntry
}

type cacheEntry struct {
	principal model.Principal
	expires   time.Time
}

func New(store Store, jwtSecret, apiKeyPepper, issuer, audience string, ttl time.Duration, max int) *Authenticator {
	if apiKeyPepper == "" {
		apiKeyPepper = jwtSecret
	}
	return &Authenticator{
		store:        store,
		jwtSecret:    jwtSecret,
		apiKeyPepper: apiKeyPepper,
		issuer:       issuer,
		audience:     audience,
		ttl:          ttl,
		max:          max,
		cache:        map[string]cacheEntry{},
	}
}

func APIKeyHash(key, pepper string) string {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(key))
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

func (a *Authenticator) Invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache = map[string]cacheEntry{}
}

type principalKey struct{}

func PrincipalFromContext(ctx context.Context) (model.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(model.Principal)
	return p, ok
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.Authenticate(r.Header.Get("Authorization"))
		if err != nil {
			writeAuthError(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func (a *Authenticator) Authenticate(header string) (model.Principal, error) {
	if header == "" {
		return model.Principal{}, ErrUnauthorized
	}
	if strings.HasPrefix(header, "ApiKey ") {
		key := strings.TrimSpace(strings.TrimPrefix(header, "ApiKey "))
		if key == "" {
			return model.Principal{}, ErrUnauthorized
		}
		hash := APIKeyHash(key, a.apiKeyPepper)
		if p, ok := a.getCache("apikey:" + hash); ok {
			return p, nil
		}
		p, ok := a.store.ResolveAPIKeyHash(hash)
		if !ok {
			return model.Principal{}, ErrUnauthorized
		}
		a.setCache("apikey:"+hash, p)
		return p, nil
	}
	if strings.HasPrefix(header, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		sub, err := a.verifyJWT(token)
		if err != nil {
			return model.Principal{}, ErrUnauthorized
		}
		if p, ok := a.getCache("jwt:" + sub); ok {
			return p, nil
		}
		p, ok := a.store.ResolveJWTSubject(sub)
		if !ok {
			return model.Principal{}, ErrUnauthorized
		}
		p.AuthMethod = "jwt"
		a.setCache("jwt:"+sub, p)
		return p, nil
	}
	return model.Principal{}, ErrUnauthorized
}

func (a *Authenticator) getCache(k string) (model.Principal, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ent, ok := a.cache[k]
	if !ok || time.Now().After(ent.expires) {
		delete(a.cache, k)
		return model.Principal{}, false
	}
	return ent.principal, true
}

func (a *Authenticator) setCache(k string, p model.Principal) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.max > 0 && len(a.cache) >= a.max {
		for old := range a.cache {
			delete(a.cache, old)
			break
		}
	}
	a.cache[k] = cacheEntry{principal: p, expires: time.Now().Add(a.ttl)}
}

type jwtClaims struct {
	Subject   string `json:"sub"`
	ExpiresAt int64  `json:"exp"`
	NotBefore int64  `json:"nbf"`
	Issuer    string `json:"iss"`
	Audience  any    `json:"aud"`
}

func (a *Authenticator) verifyJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || a.jwtSecret == "" {
		return "", ErrUnauthorized
	}
	signed := parts[0] + "." + parts[1]
	wantMAC := hmac.New(sha256.New, []byte(a.jwtSecret))
	wantMAC.Write([]byte(signed))
	want := wantMAC.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(got, want) {
		return "", ErrUnauthorized
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ErrUnauthorized
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", ErrUnauthorized
	}
	now := time.Now().Unix()
	if claims.Subject == "" || (claims.ExpiresAt != 0 && now >= claims.ExpiresAt) || (claims.NotBefore != 0 && now < claims.NotBefore) {
		return "", ErrUnauthorized
	}
	if a.issuer != "" && claims.Issuer != a.issuer {
		return "", ErrUnauthorized
	}
	if a.audience != "" && !audienceMatches(claims.Audience, a.audience) {
		return "", ErrUnauthorized
	}
	return claims.Subject, nil
}

func audienceMatches(v any, want string) bool {
	switch aud := v.(type) {
	case string:
		return aud == want
	case []any:
		for _, item := range aud {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func writeAuthError(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"UNAUTHORIZED","message":"unauthorized","request_id":"` + RequestID(r) + `"}`))
}

func RequestID(r *http.Request) string {
	if v := r.Context().Value(requestIDKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

type requestIDKey struct{}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}
