package mqtt

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	broker "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"golang.org/x/net/websocket"
)

func TestMatchCommonNameTable(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"*.region-1.prod.example.net", "emqtt-01.region-1.prod.example.net", true},
		{"*.region-1.prod.example.net", "broker-7f3a91c2-pub.region-1.prod.example.net", true},
		{"*.region-1.prod.example.net", "EMQTT-01.Region-1.Prod.Example.Net.", true},
		{"*.region-1.prod.example.net", "emqtt-prod.example.net", false},
		{"*.region-1.prod.example.net", "a.b.region-1.prod.example.net", false},
		{"*.region-1.prod.example.net", "region-1.prod.example.net", false},
		{"*.region-1.prod.example.net", ".region-1.prod.example.net", false},
		{"broker.example.net", "broker.example.net", true},
		{"broker.example.net", "other.example.net", false},
		{"emqtt*.example.net", "emqtt-01.example.net", false}, // partial wildcard
		{"*.*.example.net", "a.b.example.net", false},
		{"*.net", "example.net", false}, // needs two labels after *
		{"*.example.net", "192.0.2.1", false},
		{"", "broker.example.net", false},
	}
	for _, c := range cases {
		if got := matchCommonName(c.pattern, c.host); got != c.want {
			t.Errorf("matchCommonName(%q, %q) = %v, want %v", c.pattern, c.host, got, c.want)
		}
	}
}

type testPKI struct {
	pem  []byte
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	der  []byte
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testPKI{pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert, key, der}
}

// issue signs a leaf from tmpl; defaults give a valid server-auth certificate.
func (p *testPKI) issue(t *testing.T, tmpl *x509.Certificate) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.SerialNumber == nil {
		tmpl.SerialNumber = big.NewInt(2)
	}
	if tmpl.NotBefore.IsZero() {
		tmpl.NotBefore = time.Now().Add(-time.Hour)
	}
	if tmpl.NotAfter.IsZero() {
		tmpl.NotAfter = time.Now().Add(time.Hour)
	}
	if tmpl.ExtKeyUsage == nil {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.cert, &key.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func cnLeaf(cn string, dns ...string) *x509.Certificate {
	return &x509.Certificate{Subject: pkix.Name{CommonName: cn}, DNSNames: dns}
}

// serveTLS starts an MQTT TLS broker (or wss when ws) and returns the broker URL.
func serveTLS(t *testing.T, cert tls.Certificate, ws bool) string {
	t.Helper()
	b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	_ = b.AddHook(new(auth.AllowHook), nil)
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if ws {
		_ = b.Serve()
		server := httptest.NewUnstartedServer(websocket.Handler(func(c *websocket.Conn) {
			c.PayloadType = websocket.BinaryFrame
			_ = b.EstablishConnection("ws", c)
		}))
		server.TLS = cfg
		server.StartTLS()
		t.Cleanup(func() { server.Close(); b.Close() })
		return "wss" + strings.TrimPrefix(server.URL, "https")
	}
	l := listeners.NewTCP(listeners.Config{ID: "tls", Address: "127.0.0.1:0", TLSConfig: cfg})
	if err := b.AddListener(l); err != nil {
		t.Fatal(err)
	}
	_ = b.Serve()
	t.Cleanup(func() { b.Close() })
	return "mqtts://" + l.Address()
}

const testServerName = "emqtt-01.region-1.prod.example.net"

// connectAll dials with every transport and protocol version and calls check
// with the outcome; check receives a nil error when the connection succeeded.
func connectAll(t *testing.T, cert tls.Certificate, tlsCfg scenario.TLS, check func(t *testing.T, err error)) {
	t.Helper()
	for _, ws := range []bool{false, true} {
		url := serveTLS(t, cert, ws)
		for _, v := range []string{"3.1.1", "5"} {
			name := "mqtts-" + v
			if ws {
				name = "wss-" + v
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				cfg := scenario.Broker{URL: url, Version: v, CleanStart: true, TLS: tlsCfg}
				c, err := Connect(ctx, cfg, "verify", func([]byte) {})
				if err == nil {
					c.Close()
				}
				check(t, err)
			})
		}
	}
}

func wantOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantAs[T error](t *testing.T) func(*testing.T, error) {
	return func(t *testing.T, err error) {
		var target T
		if !errors.As(err, &target) {
			t.Fatalf("error %v (%T), want %T", err, err, target)
		}
	}
}

func TestCNOnlyCertificateDefaultSANFails(t *testing.T) {
	pki := newTestPKI(t)
	cert := pki.issue(t, cnLeaf("*.region-1.prod.example.net"))
	connectAll(t, cert, scenario.TLS{CAPEM: string(pki.pem), ServerName: testServerName}, func(t *testing.T, err error) {
		if err == nil || !strings.Contains(err.Error(), "legacy Common Name field") {
			t.Fatalf("error = %v, want legacy Common Name field", err)
		}
	})
}

func TestCNFallbackAcceptsCNOnlyCertificate(t *testing.T) {
	pki := newTestPKI(t)
	cert := pki.issue(t, cnLeaf("*.region-1.prod.example.net"))
	connectAll(t, cert, scenario.TLS{CAPEM: string(pki.pem), ServerName: testServerName, HostnameVerification: "cn-fallback"}, func(t *testing.T, err error) { wantOK(t, err) })
}

func TestCNFallbackRejectsWrongHost(t *testing.T) {
	pki := newTestPKI(t)
	cert := pki.issue(t, cnLeaf("*.region-1.prod.example.net"))
	connectAll(t, cert, scenario.TLS{CAPEM: string(pki.pem), ServerName: "emqtt-prod.example.net", HostnameVerification: "cn-fallback"}, wantAs[*HostnameMismatchError](t))
}

func TestSANPresentDisablesCNFallback(t *testing.T) {
	pki := newTestPKI(t)
	cert := pki.issue(t, cnLeaf("*.region-1.prod.example.net", "other.example.net"))
	connectAll(t, cert, scenario.TLS{CAPEM: string(pki.pem), ServerName: testServerName, HostnameVerification: "cn-fallback"}, wantAs[*HostnameMismatchError](t))
	connectAll(t, cert, scenario.TLS{CAPEM: string(pki.pem), ServerName: "other.example.net", HostnameVerification: "cn-fallback"}, func(t *testing.T, err error) { wantOK(t, err) })
}

func TestCNFallbackRejectsWrongCAAndExpired(t *testing.T) {
	pki, other := newTestPKI(t), newTestPKI(t)
	tlsCfg := scenario.TLS{CAPEM: string(pki.pem), ServerName: testServerName, HostnameVerification: "cn-fallback"}
	connectAll(t, other.issue(t, cnLeaf("*.region-1.prod.example.net")), tlsCfg, func(t *testing.T, err error) {
		var ce *ChainError
		var ua x509.UnknownAuthorityError
		if !errors.As(err, &ce) || !errors.As(err, &ua) {
			t.Fatalf("error = %v, want ChainError wrapping UnknownAuthorityError", err)
		}
	})
	expired := cnLeaf("*.region-1.prod.example.net")
	expired.NotBefore, expired.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	connectAll(t, pki.issue(t, expired), tlsCfg, func(t *testing.T, err error) {
		var ce *ChainError
		var ci x509.CertificateInvalidError
		if !errors.As(err, &ce) || !errors.As(err, &ci) || ci.Reason != x509.Expired {
			t.Fatalf("error = %v, want ChainError wrapping Expired", err)
		}
	})
}

func TestCNFallbackIgnoresUnrelatedIntermediates(t *testing.T) {
	pki, other := newTestPKI(t), newTestPKI(t)
	cert := pki.issue(t, cnLeaf("*.region-1.prod.example.net"))
	cert.Certificate = append(cert.Certificate, other.der)
	connectAll(t, cert, scenario.TLS{CAPEM: string(pki.pem), ServerName: testServerName, HostnameVerification: "cn-fallback"}, func(t *testing.T, err error) { wantOK(t, err) })
}

func TestHostnameVerificationNoneChecksChainOnly(t *testing.T) {
	pki, other := newTestPKI(t), newTestPKI(t)
	connectAll(t, pki.issue(t, cnLeaf("*.region-1.prod.example.net")), scenario.TLS{CAPEM: string(pki.pem), ServerName: "anything.invalid", HostnameVerification: "none"}, func(t *testing.T, err error) { wantOK(t, err) })
	connectAll(t, other.issue(t, cnLeaf("*.region-1.prod.example.net")), scenario.TLS{CAPEM: string(pki.pem), ServerName: "anything.invalid", HostnameVerification: "none"}, wantAs[*ChainError](t))
}

func TestServerAuthUsageRequired(t *testing.T) {
	pki := newTestPKI(t)
	leaf := cnLeaf("*.region-1.prod.example.net")
	leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	connectAll(t, pki.issue(t, leaf), scenario.TLS{CAPEM: string(pki.pem), ServerName: testServerName, HostnameVerification: "cn-fallback"}, wantAs[*ChainError](t))
}
