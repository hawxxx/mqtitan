package authn

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// sealer encrypts and authenticates cookie payloads with AES-256-GCM. The
// cookie name is bound as additional data so one cookie cannot stand in for
// another.
type sealer struct{ aead cipher.AEAD }

func newSealer(secret string) (*sealer, error) {
	key := make([]byte, 32)
	if secret == "" {
		// Sessions then end when the controller restarts.
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
	} else {
		if len(secret) < 32 {
			return nil, errors.New("OIDC cookie secret must be at least 32 characters")
		}
		sum := sha256.Sum256([]byte(secret))
		key = sum[:]
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead}, nil
}

func (s *sealer) seal(name string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, []byte(name))), nil
}

func (s *sealer) open(name, value string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return errors.New("invalid cookie")
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(name))
	if err != nil {
		return errors.New("invalid cookie")
	}
	return json.Unmarshal(plain, v)
}

type flowState struct {
	State, Nonce, Verifier, Next string
	Expires                      int64
}

func (a *Authenticator) cookieName(base string) string {
	if a.secure {
		return "__Host-" + base
	}
	return base
}

func (a *Authenticator) setCookie(w http.ResponseWriter, base, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: a.cookieName(base), Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
}

// session returns the identity in a valid session cookie. Cookie-authenticated
// mutations must come from the controller's own origin: SameSite=Lax already
// withholds the cookie from cross-site subresource requests, and this closes
// same-site sibling hosts as well.
func (a *Authenticator) session(r *http.Request) *Identity {
	if a.seal == nil {
		return nil
	}
	name := a.cookieName("mqtitan_session")
	cookie, err := r.Cookie(name)
	if err != nil {
		return nil
	}
	var id Identity
	if a.seal.open(name, cookie.Value, &id) != nil || id.Method != "oidc" || time.Now().Unix() >= id.Expires {
		return nil
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOriginRequest(r) {
		return nil
	}
	if len(a.c.OIDCAllowedGroups) > 0 {
		member := false
		for _, g := range id.Groups {
			member = member || contains(a.c.OIDCAllowedGroups, g)
		}
		if !member {
			return nil
		}
	}
	return &id
}

func sameOriginRequest(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return origin == "http://"+r.Host || origin == "https://"+r.Host
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// safeNext keeps post-login redirects on this controller.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	return next
}

// Routes serves /auth/session, /auth/login, /auth/callback and /auth/logout.
func (a *Authenticator) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/session", func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.identify(r)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]any{"data": map[string]any{"enabled": a.c.Enabled(), "login": a.oauth != nil, "authenticated": ok, "user": id}})
	})
	mux.HandleFunc("GET /auth/login", func(w http.ResponseWriter, r *http.Request) {
		if a.oauth == nil {
			http.NotFound(w, r)
			return
		}
		flow := flowState{State: randomString(), Nonce: randomString(), Verifier: oauth2.GenerateVerifier(), Next: safeNext(r.URL.Query().Get("next")), Expires: time.Now().Add(10 * time.Minute).Unix()}
		value, err := a.seal.seal(a.cookieName("mqtitan_oidc"), flow)
		if err != nil {
			http.Error(w, "sign-in unavailable", 500)
			return
		}
		a.setCookie(w, "mqtitan_oidc", value, 600)
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, a.oauth.AuthCodeURL(flow.State, oidc.Nonce(flow.Nonce), oauth2.S256ChallengeOption(flow.Verifier)), http.StatusFound)
	})
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if a.oauth == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		name := a.cookieName("mqtitan_oidc")
		cookie, err := r.Cookie(name)
		var flow flowState
		if err != nil || a.seal.open(name, cookie.Value, &flow) != nil || time.Now().Unix() >= flow.Expires {
			http.Error(w, "sign-in expired; start again from the dashboard", 400)
			return
		}
		a.setCookie(w, "mqtitan_oidc", "", -1)
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(flow.State)) != 1 {
			http.Error(w, "sign-in state mismatch", 400)
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, "identity provider rejected sign-in: "+e, 401)
			return
		}
		ctx := oidc.ClientContext(r.Context(), a.client)
		token, err := a.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(flow.Verifier))
		if err != nil {
			http.Error(w, "authorization code exchange failed", 502)
			return
		}
		raw, _ := token.Extra("id_token").(string)
		if raw == "" {
			http.Error(w, "identity provider returned no ID token", 502)
			return
		}
		id, err := a.verifyToken(ctx, raw, flow.Nonce)
		if errors.Is(err, errNotMember) {
			http.Error(w, "your account is not in a group allowed to use MQTiTan", 403)
			return
		}
		if err != nil {
			http.Error(w, "ID token verification failed", 401)
			return
		}
		id.Expires = time.Now().Add(a.c.OIDCSessionTTL).Unix()
		value, err := a.seal.seal(a.cookieName("mqtitan_session"), id)
		if err != nil || len(value) > 3800 {
			http.Error(w, "session too large; restrict MQTITAN_OIDC_ALLOWED_GROUPS", 500)
			return
		}
		a.setCookie(w, "mqtitan_session", value, int(a.c.OIDCSessionTTL.Seconds()))
		http.Redirect(w, r, flow.Next, http.StatusFound)
	})
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginRequest(r) {
			writeJSON(w, 403, map[string]any{"error": map[string]string{"code": "ORIGIN_REJECTED", "message": "cross-origin logout rejected"}})
			return
		}
		redirect := "/"
		if a.oauth != nil {
			a.setCookie(w, "mqtitan_session", "", -1)
			if a.endSession != "" {
				q := url.Values{"client_id": {a.c.OIDCClientID}}
				if a.c.OIDCPostLogoutRedirectURL != "" {
					q.Set("post_logout_redirect_uri", a.c.OIDCPostLogoutRedirectURL)
				}
				sep := "?"
				if strings.Contains(a.endSession, "?") {
					sep = "&"
				}
				redirect = a.endSession + sep + q.Encode()
			}
		}
		writeJSON(w, 200, map[string]any{"data": map[string]string{"redirect": redirect}})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func randomString() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
