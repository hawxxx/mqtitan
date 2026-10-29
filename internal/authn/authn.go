package authn

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Token, BasicUsername, BasicPassword, OIDCIssuer, OIDCClientID, ProxyHeader string
	TrustedProxyCIDRs                                                          []string
	// OIDCRedirectURL enables browser sign-in (authorization code flow with
	// PKCE). It must be the public /auth/callback URL registered at the
	// identity provider. OIDCClientSecret is optional for public clients.
	OIDCClientSecret, OIDCRedirectURL, OIDCPostLogoutRedirectURL string
	// OIDCScopes defaults to openid, profile and email.
	OIDCScopes []string
	// OIDCAudiences lists extra audiences accepted on bearer tokens, for
	// access tokens minted for an API or another client. The client ID is
	// always accepted.
	OIDCAudiences []string
	// OIDCAllowedGroups, when set, requires the groups claim to contain at
	// least one of these values for both browser sessions and bearer tokens.
	OIDCAllowedGroups                  []string
	OIDCGroupsClaim, OIDCUsernameClaim string
	OIDCCookieSecret, OIDCCAFile       string
	OIDCSessionTTL                     time.Duration
}
type Authorize func(*http.Request) bool

func (c Config) Enabled() bool {
	return c.Token != "" || c.BasicUsername != "" || c.BasicPassword != "" || c.OIDCIssuer != "" || c.ProxyHeader != ""
}

// Authenticator checks every configured credential type. Routes serves the
// browser sign-in endpoints under /auth/.
type Authenticator struct {
	c          Config
	verifier   *oidc.IDTokenVerifier
	audiences  map[string]bool
	trusted    []*net.IPNet
	client     *http.Client
	oauth      *oauth2.Config
	endSession string
	seal       *sealer
	secure     bool
}

func New(ctx context.Context, c Config) (*Authenticator, error) {
	if (c.BasicUsername == "") != (c.BasicPassword == "") {
		return nil, errors.New("basic authentication requires username and password")
	}
	if (c.OIDCIssuer == "") != (c.OIDCClientID == "") {
		return nil, errors.New("OIDC requires issuer and client ID")
	}
	a := &Authenticator{c: c, audiences: map[string]bool{c.OIDCClientID: true}}
	for _, aud := range c.OIDCAudiences {
		a.audiences[aud] = true
	}
	if a.c.OIDCGroupsClaim == "" {
		a.c.OIDCGroupsClaim = "groups"
	}
	if a.c.OIDCUsernameClaim == "" {
		a.c.OIDCUsernameClaim = "preferred_username"
	}
	if a.c.OIDCSessionTTL <= 0 {
		a.c.OIDCSessionTTL = 8 * time.Hour
	}
	if c.OIDCIssuer == "" && (c.OIDCRedirectURL != "" || c.OIDCClientSecret != "" || len(c.OIDCAllowedGroups) > 0) {
		return nil, errors.New("OIDC settings require MQTITAN_OIDC_ISSUER")
	}
	if c.OIDCIssuer != "" {
		if err := a.setupOIDC(ctx); err != nil {
			return nil, err
		}
	}
	for _, cidr := range c.TrustedProxyCIDRs {
		_, n, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil {
			return nil, errors.New("invalid trusted proxy CIDR")
		}
		a.trusted = append(a.trusted, n)
	}
	if c.ProxyHeader != "" && len(a.trusted) == 0 {
		return nil, errors.New("reverse-proxy authentication requires trusted proxy CIDRs")
	}
	return a, nil
}

// secureURL accepts HTTPS, and plain HTTP only for loopback hosts so a local
// identity provider can be used in development.
func secureURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, false
	}
	if u.Scheme == "https" {
		return u, true
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return u, u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback()))
}

func (a *Authenticator) setupOIDC(ctx context.Context) error {
	c := a.c
	if _, ok := secureURL(c.OIDCIssuer); !ok {
		return errors.New("OIDC issuer requires HTTPS")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if c.OIDCCAFile != "" {
		pem, err := os.ReadFile(c.OIDCCAFile)
		if err != nil {
			return fmt.Errorf("OIDC CA file unreadable: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("OIDC CA file contains no certificates")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	a.client = &http.Client{Transport: transport, Timeout: 15 * time.Second}
	ctx = oidc.ClientContext(ctx, a.client)
	provider, err := oidc.NewProvider(ctx, c.OIDCIssuer)
	if err != nil {
		return fmt.Errorf("OIDC discovery failed for %s: %w", c.OIDCIssuer, err)
	}
	// Audiences are checked against the configured list instead of only the
	// client ID, so API access tokens can be accepted as well.
	a.verifier = provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = provider.Claims(&meta)
	a.endSession = meta.EndSession
	if c.OIDCRedirectURL == "" {
		if c.OIDCPostLogoutRedirectURL != "" {
			return errors.New("OIDC post-logout redirect requires MQTITAN_OIDC_REDIRECT_URL")
		}
		return nil
	}
	redirect, ok := secureURL(c.OIDCRedirectURL)
	if !ok {
		return errors.New("OIDC redirect URL requires HTTPS")
	}
	if redirect.Path != "/auth/callback" {
		return errors.New("OIDC redirect URL path must be /auth/callback")
	}
	if c.OIDCPostLogoutRedirectURL != "" {
		if _, ok := secureURL(c.OIDCPostLogoutRedirectURL); !ok {
			return errors.New("OIDC post-logout redirect URL requires HTTPS")
		}
	}
	a.secure = redirect.Scheme == "https"
	scopes := c.OIDCScopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	} else if !contains(scopes, oidc.ScopeOpenID) {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	a.oauth = &oauth2.Config{ClientID: c.OIDCClientID, ClientSecret: c.OIDCClientSecret, Endpoint: provider.Endpoint(), RedirectURL: c.OIDCRedirectURL, Scopes: scopes}
	a.seal, err = newSealer(c.OIDCCookieSecret)
	return err
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Authorize reports whether the request carries any accepted credential.
func (a *Authenticator) Authorize(r *http.Request) bool {
	_, ok := a.identify(r)
	return ok
}

func (a *Authenticator) identify(r *http.Request) (*Identity, bool) {
	c := a.c
	if !c.Enabled() {
		return nil, true
	}
	header := r.Header.Get("Authorization")
	if value, ok := strings.CutPrefix(header, "Bearer "); ok {
		if c.Token != "" && subtle.ConstantTimeCompare([]byte(value), []byte(c.Token)) == 1 {
			return nil, true
		}
		if a.verifier != nil {
			if id, err := a.verifyToken(r.Context(), value, ""); err == nil {
				return id, true
			}
		}
	}
	if c.BasicUsername != "" {
		u, p, ok := r.BasicAuth()
		if ok && subtle.ConstantTimeCompare([]byte(u), []byte(c.BasicUsername)) == 1 && subtle.ConstantTimeCompare([]byte(p), []byte(c.BasicPassword)) == 1 {
			return &Identity{Subject: u, Name: u, Method: "basic"}, true
		}
	}
	if id := a.session(r); id != nil {
		return id, true
	}
	if c.ProxyHeader != "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if user := r.Header.Get(c.ProxyHeader); err == nil && user != "" {
			ip := net.ParseIP(host)
			for _, n := range a.trusted {
				if n.Contains(ip) {
					return &Identity{Subject: user, Name: user, Method: "proxy"}, true
				}
			}
		}
	}
	return nil, false
}

// Identity describes an authenticated OIDC, basic or proxy user.
type Identity struct {
	Subject string   `json:"subject"`
	Name    string   `json:"name,omitempty"`
	Email   string   `json:"email,omitempty"`
	Groups  []string `json:"groups,omitempty"`
	Method  string   `json:"method"`
	Expires int64    `json:"expires,omitempty"`
}

// verifyToken checks signature, issuer, expiry, audience, the nonce when one
// is expected, and the group allow-list.
func (a *Authenticator) verifyToken(ctx context.Context, raw, nonce string) (*Identity, error) {
	tok, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, err
	}
	audience := false
	for _, aud := range tok.Audience {
		if a.audiences[aud] {
			audience = true
		}
	}
	if !audience {
		return nil, errors.New("token audience not accepted")
	}
	if nonce != "" && subtle.ConstantTimeCompare([]byte(tok.Nonce), []byte(nonce)) != 1 {
		return nil, errors.New("token nonce mismatch")
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return nil, err
	}
	id := &Identity{Subject: tok.Subject, Method: "oidc", Expires: tok.Expiry.Unix()}
	id.Name, _ = claims[a.c.OIDCUsernameClaim].(string)
	if id.Name == "" {
		id.Name, _ = claims["name"].(string)
	}
	id.Email, _ = claims["email"].(string)
	groups := claimStrings(claims[a.c.OIDCGroupsClaim])
	if len(a.c.OIDCAllowedGroups) > 0 {
		for _, g := range groups {
			if contains(a.c.OIDCAllowedGroups, g) {
				id.Groups = append(id.Groups, g)
			}
		}
		if len(id.Groups) == 0 {
			return nil, errNotMember
		}
	}
	return id, nil
}

var errNotMember = errors.New("user is not in an allowed group")

// claimStrings accepts a JSON array of strings or a single space- or
// comma-separated string, which covers the common IdP group claim shapes.
func claimStrings(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return strings.FieldsFunc(t, func(r rune) bool { return r == ' ' || r == ',' })
	}
	return nil
}
