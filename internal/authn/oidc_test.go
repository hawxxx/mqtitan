package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// fakeIdP is a minimal OIDC provider: discovery, JWKS, authorization code
// token endpoint with PKCE, and an end-session endpoint.
type fakeIdP struct {
	*httptest.Server
	key    *rsa.PrivateKey
	groups []string
	nonce  string
	// challenge received on the authorization request
	challenge string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, groups: []string{"mqtitan-users"}}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": f.URL, "authorization_endpoint": f.URL + "/authorize", "token_endpoint": f.URL + "/token", "jwks_uri": f.URL + "/jwks", "end_session_endpoint": f.URL + "/end-session", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") == "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "opaque", "token_type": "Bearer", "id_token": f.sign(t, map[string]any{"aud": "mqtitan", "nonce": f.nonce, "groups": f.groups})})
	})
	t.Cleanup(f.Close)
	return f
}

func (f *fakeIdP) sign(t *testing.T, extra map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: f.key, KeyID: "k1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": f.URL, "sub": "user-1", "preferred_username": "alice", "email": "alice@example.com", "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}
	for k, v := range extra {
		claims[k] = v
	}
	body, _ := json.Marshal(claims)
	jws, err := signer.Sign(body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func oidcConfig(f *fakeIdP) Config {
	return Config{OIDCIssuer: f.URL, OIDCClientID: "mqtitan", OIDCRedirectURL: "http://127.0.0.1:8080/auth/callback", OIDCAllowedGroups: []string{"mqtitan-users"}, OIDCAudiences: []string{"mqtitan-api"}}
}

func TestOIDCBearerAudienceAndGroups(t *testing.T) {
	f := newFakeIdP(t)
	a, err := New(context.Background(), oidcConfig(f))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		claims map[string]any
		want   bool
	}{
		{"client audience", map[string]any{"aud": "mqtitan", "groups": []string{"mqtitan-users"}}, true},
		{"extra audience", map[string]any{"aud": []string{"other", "mqtitan-api"}, "groups": []string{"mqtitan-users"}}, true},
		{"foreign audience", map[string]any{"aud": "other", "groups": []string{"mqtitan-users"}}, false},
		{"missing group", map[string]any{"aud": "mqtitan", "groups": []string{"staff"}}, false},
		{"expired", map[string]any{"aud": "mqtitan", "groups": []string{"mqtitan-users"}, "exp": time.Now().Add(-time.Minute).Unix()}, false},
		{"wrong issuer", map[string]any{"aud": "mqtitan", "groups": []string{"mqtitan-users"}, "iss": "https://evil.example"}, false},
	} {
		r := httptest.NewRequest("GET", "/api/v1/tests", nil)
		r.Header.Set("Authorization", "Bearer "+f.sign(t, tc.claims))
		if got := a.Authorize(r); got != tc.want {
			t.Errorf("%s: authorized=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestOIDCBrowserLoginFlow(t *testing.T) {
	f := newFakeIdP(t)
	a, err := New(context.Background(), oidcConfig(f))
	if err != nil {
		t.Fatal(err)
	}
	routes := a.Routes()

	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/login?next=/tests", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login status %d", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if !strings.HasPrefix(loc.String(), f.URL+"/authorize") || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("state") == "" || !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("bad authorization redirect %s", loc)
	}
	flow := rec.Result().Cookies()[0]
	if !flow.HttpOnly || flow.SameSite != http.SameSiteLaxMode {
		t.Fatalf("flow cookie not protected: %+v", flow)
	}
	f.nonce = q.Get("nonce")

	callback := func(state, code string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/auth/callback?state="+url.QueryEscape(state)+"&code="+code, nil)
		r.AddCookie(flow)
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, r)
		return rec
	}
	if rec := callback("forged", "good-code"); rec.Code != 400 {
		t.Fatalf("forged state accepted: %d", rec.Code)
	}
	rec = callback(q.Get("state"), "good-code")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/tests" {
		t.Fatalf("callback status %d location %q body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "mqtitan_session" {
			session = c
		}
	}
	if session == nil || session.Value == "" {
		t.Fatal("no session cookie")
	}

	get := httptest.NewRequest("GET", "/api/v1/tests", nil)
	get.AddCookie(session)
	if !a.Authorize(get) {
		t.Fatal("session rejected")
	}
	post := httptest.NewRequest("POST", "http://ctl.example/api/v1/tests", nil)
	post.AddCookie(session)
	if a.Authorize(post) {
		t.Fatal("cookie mutation without Origin accepted")
	}
	post.Header.Set("Origin", "https://evil.example")
	if a.Authorize(post) {
		t.Fatal("cross-origin cookie mutation accepted")
	}
	post.Header.Set("Origin", "http://ctl.example")
	if !a.Authorize(post) {
		t.Fatal("same-origin cookie mutation rejected")
	}
	tampered := *session
	tampered.Value = session.Value[:len(session.Value)-2] + "AA"
	bad := httptest.NewRequest("GET", "/api/v1/tests", nil)
	bad.AddCookie(&tampered)
	if a.Authorize(bad) {
		t.Fatal("tampered session accepted")
	}
	// A flow cookie cannot stand in for a session.
	swap := httptest.NewRequest("GET", "/api/v1/tests", nil)
	swap.AddCookie(&http.Cookie{Name: "mqtitan_session", Value: flow.Value})
	if a.Authorize(swap) {
		t.Fatal("flow cookie accepted as session")
	}

	rec = httptest.NewRecorder()
	who := httptest.NewRequest("GET", "/auth/session", nil)
	who.AddCookie(session)
	routes.ServeHTTP(rec, who)
	var body struct {
		Data struct {
			Login, Authenticated bool
			User                 Identity
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !body.Data.Login || !body.Data.Authenticated || body.Data.User.Name != "alice" || body.Data.User.Email != "alice@example.com" {
		t.Fatalf("session status %s", rec.Body)
	}

	rec = httptest.NewRecorder()
	out := httptest.NewRequest("POST", "http://ctl.example/auth/logout", nil)
	out.Header.Set("Origin", "http://ctl.example")
	routes.ServeHTTP(rec, out)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), f.URL+"/end-session?client_id=mqtitan") {
		t.Fatalf("logout %d %s", rec.Code, rec.Body)
	}
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 {
		t.Fatalf("logout did not clear session: %+v", c)
	}
}

func TestOIDCLoginRejectsNonMembersAndBadNonce(t *testing.T) {
	f := newFakeIdP(t)
	a, err := New(context.Background(), oidcConfig(f))
	if err != nil {
		t.Fatal(err)
	}
	routes := a.Routes()
	start := func() (*http.Cookie, url.Values) {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/login?next=//evil.example", nil))
		loc, _ := url.Parse(rec.Header().Get("Location"))
		return rec.Result().Cookies()[0], loc.Query()
	}
	finish := func(flow *http.Cookie, q url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/auth/callback?code=good-code&state="+url.QueryEscape(q.Get("state")), nil)
		r.AddCookie(flow)
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, r)
		return rec
	}
	flow, q := start()
	f.nonce = "replayed"
	if rec := finish(flow, q); rec.Code != 401 {
		t.Fatalf("nonce mismatch status %d", rec.Code)
	}
	flow, q = start()
	f.nonce, f.groups = q.Get("nonce"), []string{"staff"}
	if rec := finish(flow, q); rec.Code != 403 {
		t.Fatalf("non-member status %d", rec.Code)
	}
	flow, q = start()
	f.nonce, f.groups = q.Get("nonce"), []string{"mqtitan-users"}
	if rec := finish(flow, q); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("open redirect not neutralised: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestOIDCConfigValidation(t *testing.T) {
	f := newFakeIdP(t)
	for name, c := range map[string]Config{
		"plain http issuer":  {OIDCIssuer: "http://idp.example", OIDCClientID: "x"},
		"redirect path":      {OIDCIssuer: f.URL, OIDCClientID: "x", OIDCRedirectURL: "https://ctl.example/callback"},
		"http redirect":      {OIDCIssuer: f.URL, OIDCClientID: "x", OIDCRedirectURL: "http://ctl.example/auth/callback"},
		"short secret":       {OIDCIssuer: f.URL, OIDCClientID: "x", OIDCRedirectURL: "https://ctl.example/auth/callback", OIDCCookieSecret: "short"},
		"redirect no issuer": {OIDCRedirectURL: "https://ctl.example/auth/callback"},
		"missing CA file":    {OIDCIssuer: f.URL, OIDCClientID: "x", OIDCCAFile: "/nonexistent/ca.pem"},
	} {
		if _, err := New(context.Background(), c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	a, err := New(context.Background(), Config{OIDCIssuer: f.URL, OIDCClientID: "x", OIDCRedirectURL: "https://ctl.example/auth/callback"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.Routes().ServeHTTP(rec, httptest.NewRequest("GET", "/auth/login", nil))
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Name != "__Host-mqtitan_oidc" || !c[0].Secure {
		t.Fatalf("HTTPS deployment must use __Host- secure cookies: %+v", c)
	}
}
