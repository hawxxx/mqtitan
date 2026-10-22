package mqtt

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"net"
	"strings"
)

// ChainError reports that the peer chain did not verify against the trust roots.
type ChainError struct{ Err error }

func (e *ChainError) Error() string {
	return "TLS certificate chain verification failed: " + e.Err.Error()
}
func (e *ChainError) Unwrap() error { return e.Err }

// HostnameMismatchError reports that neither SAN nor CN matched the expected name.
type HostnameMismatchError struct{ Name, Mode string }

func (e *HostnameMismatchError) Error() string {
	return "TLS certificate does not match " + e.Name + " (hostnameVerification " + e.Mode + ")"
}

// matchCommonName compares a certificate CN with host using OpenSSL CN-fallback
// semantics: case-insensitive, a single leading "*." wildcard that covers exactly
// one label and needs at least two labels after it, and never for IP literals.
func matchCommonName(pattern, host string) bool {
	pattern = strings.TrimSuffix(strings.ToLower(pattern), ".")
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if pattern == "" || host == "" || net.ParseIP(host) != nil {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == host
	}
	rest, ok := strings.CutPrefix(pattern, "*.")
	if !ok || strings.Contains(rest, "*") || strings.Count(rest, ".") < 1 {
		return false // partial, multiple or too-broad wildcards never match
	}
	label, suffix, ok := strings.Cut(host, ".")
	return ok && label != "" && suffix == rest
}

var oidCommonName = asn1.ObjectIdentifier{2, 5, 4, 3}

// verifyPeer verifies the chain against roots with server-auth usage and, unless
// mode is "none", the name against SANs or, for certificates without any DNS or
// IP SAN, against every subject CN.
func verifyPeer(roots *x509.CertPool, mode, name string) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return &ChainError{errors.New("server sent no certificate")}
		}
		leaf := cs.PeerCertificates[0]
		inter := x509.NewCertPool()
		for _, c := range cs.PeerCertificates[1:] {
			inter.AddCert(c)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return &ChainError{err}
		}
		if mode == "none" {
			return nil
		}
		if len(leaf.DNSNames) > 0 || len(leaf.IPAddresses) > 0 {
			if leaf.VerifyHostname(name) != nil { // SAN present: no CN fallback
				return &HostnameMismatchError{name, mode}
			}
			return nil
		}
		for _, atv := range leaf.Subject.Names { // every CN attribute, as OpenSSL does
			if atv.Type.Equal(oidCommonName) {
				if cn, ok := atv.Value.(string); ok && matchCommonName(cn, name) {
					return nil
				}
			}
		}
		return &HostnameMismatchError{name, mode}
	}
}
