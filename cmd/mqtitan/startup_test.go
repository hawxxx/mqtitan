package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mqtitan/mqtitan/internal/authn"
)

// fakeIssuer serves just enough discovery metadata for authn.New to succeed.
func fakeIssuer(t *testing.T) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize", "token_endpoint": srv.URL + "/token", "jwks_uri": srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestControllerStartsWithEveryAuthCombination builds the real handler tree
// (dashboard assets plus auth routes) for each supported auth configuration.
// Route registration panics, such as ServeMux pattern conflicts, only show up
// when these pieces are wired together as the server does.
func TestControllerStartsWithEveryAuthCombination(t *testing.T) {
	issuer := fakeIssuer(t)
	cookie := strings.Repeat("c", 32)
	cases := map[string]map[string]string{
		"local mode":       {},
		"token":            {"MQTITAN_TOKEN": "t"},
		"basic":            {"MQTITAN_BASIC_USERNAME": "u", "MQTITAN_BASIC_PASSWORD": "p"},
		"proxy":            {"MQTITAN_PROXY_IDENTITY_HEADER": "X-User", "MQTITAN_TRUSTED_PROXY_CIDRS": "10.0.0.0/8"},
		"oidc bearer only": {"MQTITAN_OIDC_ISSUER": issuer, "MQTITAN_OIDC_CLIENT_ID": "c"},
		"oidc browser":     {"MQTITAN_OIDC_ISSUER": issuer, "MQTITAN_OIDC_CLIENT_ID": "c", "MQTITAN_OIDC_REDIRECT_URL": "https://ctl.example/auth/callback", "MQTITAN_OIDC_COOKIE_SECRET": cookie},
		"oidc and token":   {"MQTITAN_TOKEN": "t", "MQTITAN_OIDC_ISSUER": issuer, "MQTITAN_OIDC_CLIENT_ID": "c", "MQTITAN_OIDC_REDIRECT_URL": "https://ctl.example/auth/callback", "MQTITAN_OIDC_ALLOWED_GROUPS": "a,b"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			for _, k := range []string{"MQTITAN_TOKEN", "MQTITAN_BASIC_USERNAME", "MQTITAN_BASIC_PASSWORD", "MQTITAN_PROXY_IDENTITY_HEADER", "MQTITAN_TRUSTED_PROXY_CIDRS", "MQTITAN_OIDC_ISSUER", "MQTITAN_OIDC_CLIENT_ID", "MQTITAN_OIDC_REDIRECT_URL", "MQTITAN_OIDC_COOKIE_SECRET", "MQTITAN_OIDC_ALLOWED_GROUPS"} {
				t.Setenv(k, "")
			}
			for k, v := range env {
				t.Setenv(k, v)
			}
			web := t.TempDir()
			if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<html></html>"), 0600); err != nil {
				t.Fatal(err)
			}
			l, err := openServer(runOptions{Listen: "127.0.0.1:0", Data: filepath.Join(t.TempDir(), "t.db"), Web: web})
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			for _, path := range []string{"/", "/auth/session"} {
				resp, err := http.Get("http://" + l.address + path)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
				}
			}
		})
	}
}

func TestAuthWarningsFlagMissingCookieSecret(t *testing.T) {
	if got := authWarnings(authn.Config{OIDCRedirectURL: "https://ctl.example/auth/callback"}); len(got) != 1 || !strings.Contains(got[0], "MQTITAN_OIDC_COOKIE_SECRET") {
		t.Fatalf("missing cookie secret warnings = %v", got)
	}
	if got := authWarnings(authn.Config{OIDCRedirectURL: "https://ctl.example/auth/callback", OIDCCookieSecret: strings.Repeat("c", 32)}); len(got) != 0 {
		t.Fatalf("unexpected warnings %v", got)
	}
	if got := authWarnings(authn.Config{OIDCIssuer: "https://idp.example", OIDCClientID: "c"}); len(got) != 0 {
		t.Fatalf("bearer-only OIDC needs no cookie secret: %v", got)
	}
}
