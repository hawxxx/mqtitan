package worker_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	broker "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mqtitan/mqtitan/internal/api"
	"github.com/mqtitan/mqtitan/internal/storage"
	"github.com/mqtitan/mqtitan/internal/worker"
	"golang.org/x/net/websocket"
)

func mutualTLSFixture(t *testing.T) (string, string, string) {
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
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "mqtt.example.com"}, DNSNames: []string{"mqtt.example.com"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
}

// Exercises upload, encrypted persistence, profile resolution, worker transfer, WSS and mTLS on real sockets.
func TestUploadedCertificateRunsWSSMutualTLSLocallyAndOnWorker(t *testing.T) {
	for _, version := range []string{"3.1.1", "5"} {
		for _, distributed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/distributed=%t", version, distributed), func(t *testing.T) {
				store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				m, err := api.NewManager(store)
				if err != nil {
					t.Fatal(err)
				}
				defer m.Close()
				h := api.Handler(m, api.Options{Token: "controller-secret"})
				ca, cert, key := mutualTLSFixture(t)
				pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
				if err != nil {
					t.Fatal(err)
				}
				roots := x509.NewCertPool()
				roots.AppendCertsFromPEM([]byte(ca))
				b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
				if err = b.AddHook(new(auth.AllowHook), nil); err != nil {
					t.Fatal(err)
				}
				if err = b.Serve(); err != nil {
					t.Fatal(err)
				}
				defer b.Close()
				endpoint := httptest.NewUnstartedServer(websocket.Handler(func(c *websocket.Conn) { c.PayloadType = websocket.BinaryFrame; _ = b.EstablishConnection("wss", c) }))
				endpoint.Config.ErrorLog = slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError)
				endpoint.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
				endpoint.StartTLS()
				defer endpoint.Close()
				upload, _ := json.Marshal(map[string]string{"name": "WSS client", "caPem": ca, "certPem": cert, "keyPem": key})
				request := httptest.NewRequest("POST", "/api/v1/certificates", strings.NewReader(string(upload)))
				request.Header.Set("Authorization", "Bearer controller-secret")
				response := httptest.NewRecorder()
				h.ServeHTTP(response, request)
				if response.Code != 201 {
					t.Fatalf("upload returned %d", response.Code)
				}
				var profile struct {
					Data struct {
						ID string `json:"id"`
					} `json:"data"`
				}
				if err = json.Unmarshal(response.Body.Bytes(), &profile); err != nil {
					t.Fatal(err)
				}
				raw := fmt.Sprintf(`apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: Uploaded WSS certificates
broker:
  url: %s/mqtt
  version: "%s"
  connectTimeout: 1s
  keepAlive: 30s
  cleanStart: true
  tls:
    profile: %s
    serverName: mqtt.example.com
clients:
  count: 1
  idTemplate: tls-${sequence}
stages:
  - duration: 1500ms
    targetClients: 1
workloads:
  - name: publisher
    type: publisher
    clients: 1
    topic: tls/test
    qos: 1
    ratePerClient: 20
    payload:
      type: static
      value: hello
`, "wss"+strings.TrimPrefix(endpoint.URL, "https"), version, profile.Data.ID)
				var ids []string
				if distributed {
					controller := httptest.NewServer(h)
					defer controller.Close()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					done := make(chan error, 1)
					go func() {
						done <- worker.Run(ctx, worker.Options{Controller: controller.URL, ID: "tls-worker", Token: "controller-secret"})
					}()
					defer func() {
						cancel()
						select {
						case <-done:
						case <-time.After(3 * time.Second):
							t.Error("worker did not stop")
						}
					}()
					ids = []string{"tls-worker"}
				}
				var run api.Test
				deadline := time.Now().Add(9 * time.Second)
				for time.Now().Before(deadline) {
					run, err = m.StartOnWorkers(raw, ids)
					if err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if err != nil {
					t.Fatal(err)
				}
				for time.Now().Before(deadline) {
					run, err = m.Get(run.ID)
					if err != nil {
						t.Fatal(err)
					}
					if run.Status != "running" {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if run.Status != "completed" || run.Snapshot.Published == 0 || run.Snapshot.ConnectErrors != 0 {
					t.Fatalf("TLS run failed: status=%s messages=%d errors=%d %s", run.Status, run.Snapshot.Published, run.Snapshot.ConnectErrors, run.Error)
				}
				if strings.Contains(run.Scenario, "PRIVATE KEY") {
					t.Fatal("private key in result")
				}
				request = httptest.NewRequest("GET", "/api/v1/tests/"+run.ID+"/export", nil)
				request.Header.Set("Authorization", "Bearer controller-secret")
				response = httptest.NewRecorder()
				h.ServeHTTP(response, request)
				if response.Code != 200 || strings.Contains(response.Body.String(), "PRIVATE KEY") {
					t.Fatal("private key in report or report failed")
				}
			})
		}
	}
}
