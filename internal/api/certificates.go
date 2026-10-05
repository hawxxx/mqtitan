package api

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mqtitan/mqtitan/internal/scenario"
)

const maxCertificatePEM = 128 << 10

type certificateInput struct {
	Name    string `json:"name"`
	CAPEM   string `json:"caPem,omitempty"`
	CertPEM string `json:"certPem,omitempty"`
	KeyPEM  string `json:"keyPem,omitempty"`
}

// CertificateProfile contains metadata only. Key material never leaves upload/execution paths.
type CertificateProfile struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	CreatedAt            time.Time `json:"createdAt"`
	HasCA                bool      `json:"hasCa"`
	HasClientCertificate bool      `json:"hasClientCertificate"`
	Subject              string    `json:"subject,omitempty"`
	ExpiresAt            time.Time `json:"expiresAt"`
	Fingerprint          string    `json:"fingerprint"`
}

type storedCertificate struct {
	CertificateProfile
	Material certificateInput `json:"material"`
}

func parseCertificatePEM(value string) ([]*x509.Certificate, error) {
	var certificates []*x509.Certificate
	rest := []byte(strings.TrimSpace(value))
	for len(rest) > 0 {
		// pem.Decode skips garbage; reject it rather than silently accepting malformed uploads.
		if !strings.HasPrefix(string(rest), "-----BEGIN CERTIFICATE-----") {
			return nil, errors.New("expected PEM certificates only")
		}
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("invalid PEM certificate")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid X.509 certificate")
		}
		now := time.Now()
		if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
			return nil, errors.New("certificate is expired or not yet valid")
		}
		certificates = append(certificates, cert)
		if len(certificates) > 64 {
			return nil, errors.New("at most 64 certificates are allowed per bundle")
		}
		rest = []byte(strings.TrimSpace(string(remaining)))
	}
	if len(certificates) == 0 {
		return nil, errors.New("empty PEM certificate bundle")
	}
	return certificates, nil
}

func validateCertificate(in certificateInput) (CertificateProfile, error) {
	p := CertificateProfile{Name: strings.TrimSpace(in.Name), HasCA: in.CAPEM != "", HasClientCertificate: in.CertPEM != ""}
	if p.Name == "" || len(p.Name) > 80 {
		return p, errors.New("certificate profile name must contain 1 to 80 characters")
	}
	for _, v := range []string{in.CAPEM, in.CertPEM, in.KeyPEM} {
		if len(v) > maxCertificatePEM {
			return p, errors.New("each PEM file must be at most 128 KiB")
		}
	}
	if !p.HasCA && !p.HasClientCertificate {
		return p, errors.New("upload a CA bundle or a client certificate and private key")
	}
	if (in.CertPEM == "") != (in.KeyPEM == "") {
		return p, errors.New("client certificate and private key must be provided together")
	}
	var primary *x509.Certificate
	if p.HasCA {
		certificates, err := parseCertificatePEM(in.CAPEM)
		if err != nil {
			return p, err
		}
		for _, c := range certificates {
			if !c.IsCA {
				return p, errors.New("CA bundle must contain CA certificates")
			}
			if p.ExpiresAt.IsZero() || c.NotAfter.Before(p.ExpiresAt) {
				p.ExpiresAt = c.NotAfter
			}
		}
		primary = certificates[0]
	}
	if p.HasClientCertificate {
		certificates, err := parseCertificatePEM(in.CertPEM)
		if err != nil {
			return p, err
		}
		if _, err = tls.X509KeyPair([]byte(in.CertPEM), []byte(in.KeyPEM)); err != nil {
			return p, errors.New("client certificate and private key are invalid or do not match; encrypted keys are not supported")
		}
		leaf := certificates[0]
		validUsage := len(leaf.ExtKeyUsage) == 0
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny {
				validUsage = true
			}
		}
		if !validUsage {
			return p, errors.New("certificate does not permit TLS client authentication")
		}
		for _, c := range certificates {
			if p.ExpiresAt.IsZero() || c.NotAfter.Before(p.ExpiresAt) {
				p.ExpiresAt = c.NotAfter
			}
		}
		primary = leaf
	}
	p.Subject = primary.Subject.String()
	fingerprint := sha256.Sum256(primary.Raw)
	p.Fingerprint = hex.EncodeToString(fingerprint[:])
	return p, nil
}

func (m *Manager) saveCertificate(in certificateInput) (CertificateProfile, error) {
	p, err := validateCertificate(in)
	if err != nil {
		return p, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return p, errors.New("controller is shutting down")
	}
	records, err := m.store.List("certificates", 1000)
	if err != nil {
		return p, err
	}
	if len(records) >= 1000 {
		return p, errors.New("certificate profile limit reached")
	}
	p.ID = ID()
	p.CreatedAt = time.Now().UTC()
	in.Name = p.Name
	raw, err := json.Marshal(storedCertificate{p, in})
	if err != nil {
		return p, err
	}
	if err = m.store.Put("certificates", p.ID, raw); err != nil {
		return p, err
	}
	return p, nil
}

func (m *Manager) certificates() ([]CertificateProfile, error) {
	records, err := m.store.List("certificates", 1000)
	if err != nil {
		return nil, err
	}
	out := make([]CertificateProfile, 0, len(records))
	for _, raw := range records {
		var p storedCertificate
		if err = json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		out = append(out, p.CertificateProfile)
	}
	return out, nil
}

func (m *Manager) resolveCertificates(s scenario.Scenario) (scenario.Scenario, error) {
	id := s.Broker.TLS.Profile
	if id == "" {
		return s, nil
	}
	if len(id) != 24 {
		return s, errors.New("invalid TLS certificate profile ID")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return s, errors.New("invalid TLS certificate profile ID")
	}
	raw, err := m.store.Get("certificates", id)
	if err != nil {
		return s, errors.New("TLS certificate profile is unavailable on this controller")
	}
	var p storedCertificate
	if err = json.Unmarshal(raw, &p); err != nil {
		return s, errors.New("TLS certificate profile cannot be read")
	}
	if _, err = validateCertificate(p.Material); err != nil {
		return s, err
	}
	s.Broker.TLS.Profile = ""
	s.Broker.TLS.CAPEM = p.Material.CAPEM
	s.Broker.TLS.CertPEM = p.Material.CertPEM
	s.Broker.TLS.KeyPEM = p.Material.KeyPEM
	return s, nil
}

func (m *Manager) certificateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/certificates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		v, err := m.certificates()
		if err != nil {
			resultError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/v1/certificates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var body certificateInput
		if !input(w, r, &body) {
			return
		}
		v, err := m.saveCertificate(body)
		if err != nil {
			writeError(w, 400, "INVALID_CERTIFICATE", err.Error())
			return
		}
		writeJSON(w, 201, v)
	})
}
