package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mqtitan/mqtitan/internal/scenario"
	"github.com/mqtitan/mqtitan/internal/storage"
)

func certificateFixture(t *testing.T) (string, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	client := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Load client"}, DNSNames: []string{"mqtt.example.com"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, client, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
}

func certificateRequest(h http.Handler, method, path string, value any, token bool) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(value)
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	if token {
		r.Header.Set("Authorization", "Bearer controller-secret")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestCertificateUploadIsValidatedEncryptedAndNeverReturned(t *testing.T) {
	m, h := setup(t)
	ca, cert, key := certificateFixture(t)
	body := map[string]string{"name": "Private lab", "caPem": ca, "certPem": cert, "keyPem": key}
	if w := certificateRequest(h, "POST", "/api/v1/certificates", body, false); w.Code != 401 {
		t.Fatalf("unauthorized: %d", w.Code)
	}
	w := certificateRequest(h, "POST", "/api/v1/certificates", body, true)
	if w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			ID                   string `json:"id"`
			HasClientCertificate bool   `json:"hasClientCertificate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.ID == "" || !response.Data.HasClientCertificate {
		t.Fatal("missing certificate metadata")
	}
	for _, method := range []string{"GET", "POST"} {
		result := w
		if method == "GET" {
			result = certificateRequest(h, "GET", "/api/v1/certificates", nil, true)
		}
		if strings.Contains(result.Body.String(), "PRIVATE KEY") || strings.Contains(result.Body.String(), "BEGIN CERTIFICATE") {
			t.Fatal("certificate material leaked")
		}
	}
	raw := strings.Replace(testScenario, "mqtt://127.0.0.1:1", "mqtts://127.0.0.1:1", 1)
	raw = strings.Replace(raw, "  cleanStart: true", "  cleanStart: true\n  tls:\n    profile: "+response.Data.ID+"\n    serverName: mqtt.example.com", 1)
	s, err := decodeScenario(raw)
	if err != nil {
		t.Fatal(err)
	}
	s, err = m.resolveCertificates(s)
	if err != nil {
		t.Fatal(err)
	}
	if s.Broker.TLS.KeyPEM != key || s.Broker.TLS.CAPEM != ca || s.Broker.TLS.Profile != "" {
		t.Fatal("profile not resolved for execution")
	}
	encoded, err := scenario.Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redact(string(encoded)), "PRIVATE KEY") {
		t.Fatal("worker material leaks into reports")
	}
	saved, err := m.SaveScenario("tls", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.YAML, response.Data.ID) || strings.Contains(saved.YAML, "PRIVATE KEY") {
		t.Fatal("scenario should retain only profile reference")
	}
}

func TestCertificateUploadRejectsBadInputs(t *testing.T) {
	_, h := setup(t)
	ca, cert, key := certificateFixture(t)
	_, _, otherKey := certificateFixture(t)
	for _, body := range []map[string]string{
		{"name": "empty"},
		{"name": "bad", "caPem": "not a certificate"},
		{"name": "partial", "certPem": cert},
		{"name": "mismatch", "certPem": cert, "keyPem": otherKey},
		{"name": "not CA", "caPem": cert},
		{"name": "oversized", "caPem": strings.Repeat("x", 128*1024+1)},
		{"name": "junk", "caPem": ca + "junk"},
		{"name": "key only", "keyPem": key},
	} {
		w := certificateRequest(h, "POST", "/api/v1/certificates", body, true)
		if w.Code != 400 {
			t.Fatalf("%s: expected 400 got %d", body["name"], w.Code)
		}
		if strings.Contains(w.Body.String(), "PRIVATE KEY") {
			t.Fatal("invalid key leaked into error")
		}
	}
}

func TestUnknownCertificateProfileFailsBeforeStarting(t *testing.T) {
	m, _ := setup(t)
	raw := strings.Replace(testScenario, "mqtt://127.0.0.1:1", "mqtts://127.0.0.1:1", 1)
	raw = strings.Replace(raw, "  cleanStart: true", "  cleanStart: true\n  tls:\n    profile: 0123456789abcdef01234567", 1)
	if _, err := m.Start(raw); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if _, err := m.SaveScenario("missing", raw); err == nil {
		t.Fatal("unknown profile saved")
	}
}

func TestCertificateProfileSurvivesRestartWithoutPlaintextOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "certificates.db")
	s, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(s)
	if err != nil {
		t.Fatal(err)
	}
	ca, cert, key := certificateFixture(t)
	p, err := m.saveCertificate(certificateInput{Name: "Restart test", CAPEM: ca, CertPEM: cert, KeyPEM: key})
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, path + "-wal"} {
		data, _ := os.ReadFile(file)
		if bytes.Contains(data, []byte("PRIVATE KEY")) || bytes.Contains(data, []byte(key)) {
			t.Fatal("plaintext key persisted")
		}
	}
	s, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err = NewManager(s)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	profiles, err := m.certificates()
	if err != nil || len(profiles) != 1 || profiles[0].ID != p.ID {
		t.Fatal("certificate profile lost across restart")
	}
	scenario, err := m.resolveCertificates(scenario.Scenario{Broker: scenario.Broker{TLS: scenario.TLS{Profile: p.ID}}})
	if err != nil || scenario.Broker.TLS.KeyPEM != key {
		t.Fatal("certificate profile cannot be decrypted after restart")
	}
}
