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
	generation   uint64
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

func (a *Authenticator) APIKeyPepper() string {
	return a.apiKeyPepper
}

func (a *Authenticator) Invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache = map[string]cacheEntry{}
	a.generation++
}

type principalKey struct{}

func PrincipalFromContext(ctx context.Context) (model.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(model.Principal)
	return p, ok
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.AuthenticateRequest(r)
		if err != nil {
			writeAuthError(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// Requests carry one credential source. Reject duplicates instead of silently
// choosing a different principal based on header precedence.
func (a *Authenticator) AuthenticateRequest(r *http.Request) (model.Principal, error) {
	credential := ""
	method := ""
	for _, name := range []string{"APIKey", "X-API-Key", "Authorization"} {
		values := r.Header.Values(name)
		if len(values) > 1 {
			return model.Principal{}, ErrUnauthorized
		}
		value := strings.TrimSpace(r.Header.Get(name))
		if value == "" {
			continue
		}
		if credential != "" {
			return model.Principal{}, ErrUnauthorized
		}
		credential = value
		method = name
	}
	if method == "APIKey" || method == "X-API-Key" {
		return a.authenticateAPIKey(credential)
	}
	return a.Authenticate(credential)
}

func (a *Authenticator) Authenticate(header string) (model.Principal, error) {
	scheme, credential, ok := strings.Cut(strings.TrimSpace(header), " ")
	credential = strings.TrimSpace(credential)
	if !ok || credential == "" {
		return model.Principal{}, ErrUnauthorized
	}
	if strings.EqualFold(scheme, "ApiKey") {
		return a.authenticateAPIKey(credential)
	}
	if !strings.EqualFold(scheme, "Bearer") {
		return model.Principal{}, ErrUnauthorized
	}
	sub, err := a.verifyJWT(credential)
	if err != nil {
		return model.Principal{}, ErrUnauthorized
	}
	return a.resolveCached("jwt:"+sub, "jwt", func() (model.Principal, bool) { return a.store.ResolveJWTSubject(sub) })
}

func (a *Authenticator) authenticateAPIKey(key string) (model.Principal, error) {
	if key == "" {
		return model.Principal{}, ErrUnauthorized
	}
	hash := APIKeyHash(key, a.apiKeyPepper)
	return a.resolveCached("apikey:"+hash, "apikey", func() (model.Principal, bool) { return a.store.ResolveAPIKeyHash(hash) })
}

func (a *Authenticator) resolveCached(key, method string, resolve func() (model.Principal, bool)) (model.Principal, error) {
	for {
		a.mu.Lock()
		if entry, ok := a.cache[key]; ok && time.Now().Before(entry.expires) {
			a.mu.Unlock()
			return entry.principal, nil
		}
		delete(a.cache, key)
		generation := a.generation
		a.mu.Unlock()
		principal, ok := resolve()
		a.mu.Lock()
		if generation != a.generation {
			a.mu.Unlock()
			continue
		}
		if !ok {
			a.mu.Unlock()
			return model.Principal{}, ErrUnauthorized
		}
		principal.AuthMethod = method
		if a.max > 0 && len(a.cache) >= a.max {
			for old := range a.cache {
				delete(a.cache, old)
				break
			}
		}
		a.cache[key] = cacheEntry{principal: principal, expires: time.Now().Add(a.ttl)}
		a.mu.Unlock()
		return principal, nil
	}
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
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrUnauthorized
	}
	var header struct {
		Algorithm string   `json:"alg"`
		Critical  []string `json:"crit"`
	}
	if json.Unmarshal(headerBytes, &header) != nil || header.Algorithm != "HS256" || len(header.Critical) > 0 {
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
	if claims.Subject == "" || (claims.ExpiresAt <= 0 || now >= claims.ExpiresAt) || (claims.NotBefore != 0 && now < claims.NotBefore) {
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
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "UNAUTHORIZED", "message": "unauthorized", "request_id": RequestID(r)})
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
