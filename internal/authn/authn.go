package authn

import (
	"context"
	"crypto/subtle"
	"errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"net"
	"net/http"
	"strings"
)

type Config struct {
	Token, BasicUsername, BasicPassword, OIDCIssuer, OIDCClientID, ProxyHeader string
	TrustedProxyCIDRs                                                          []string
}
type Authorize func(*http.Request) bool

func (c Config) Enabled() bool {
	return c.Token != "" || c.BasicUsername != "" || c.BasicPassword != "" || c.OIDCIssuer != "" || c.ProxyHeader != ""
}
func New(ctx context.Context, c Config) (Authorize, error) {
	if (c.BasicUsername == "") != (c.BasicPassword == "") {
		return nil, errors.New("basic authentication requires username and password")
	}
	if (c.OIDCIssuer == "") != (c.OIDCClientID == "") {
		return nil, errors.New("OIDC requires issuer and client ID")
	}
	var verifier *oidc.IDTokenVerifier
	if c.OIDCIssuer != "" {
		if !strings.HasPrefix(c.OIDCIssuer, "https://") {
			return nil, errors.New("OIDC issuer requires HTTPS")
		}
		provider, err := oidc.NewProvider(ctx, c.OIDCIssuer)
		if err != nil {
			return nil, errors.New("OIDC discovery failed")
		}
		verifier = provider.Verifier(&oidc.Config{ClientID: c.OIDCClientID})
	}
	trusted := make([]*net.IPNet, 0, len(c.TrustedProxyCIDRs))
	for _, cidr := range c.TrustedProxyCIDRs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, errors.New("invalid trusted proxy CIDR")
		}
		trusted = append(trusted, n)
	}
	if c.ProxyHeader != "" && len(trusted) == 0 {
		return nil, errors.New("reverse-proxy authentication requires trusted proxy CIDRs")
	}
	return func(r *http.Request) bool {
		if !c.Enabled() {
			return true
		}
		header := r.Header.Get("Authorization")
		if strings.HasPrefix(header, "Bearer ") {
			value := strings.TrimPrefix(header, "Bearer ")
			if c.Token != "" && subtle.ConstantTimeCompare([]byte(value), []byte(c.Token)) == 1 {
				return true
			}
			if verifier != nil {
				_, err := verifier.Verify(r.Context(), value)
				if err == nil {
					return true
				}
			}
		}
		if c.BasicUsername != "" {
			u, p, ok := r.BasicAuth()
			if ok && subtle.ConstantTimeCompare([]byte(u), []byte(c.BasicUsername)) == 1 && subtle.ConstantTimeCompare([]byte(p), []byte(c.BasicPassword)) == 1 {
				return true
			}
		}
		if c.ProxyHeader != "" {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err == nil && r.Header.Get(c.ProxyHeader) != "" {
				ip := net.ParseIP(host)
				for _, n := range trusted {
					if n.Contains(ip) {
						return true
					}
				}
			}
		}
		return false
	}, nil
}
