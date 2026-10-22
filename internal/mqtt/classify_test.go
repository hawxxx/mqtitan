package mqtt

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	v3packets "github.com/eclipse/paho.mqtt.golang/packets"
	broker "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	mochi "github.com/mochi-mqtt/server/v2/packets"
	"github.com/mqtitan/mqtitan/internal/scenario"
)

func TestClassifyTable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Failure
	}{
		{"deadline in tcp stage", &StageError{StageTCP, context.DeadlineExceeded}, Failure{StageTimeout, -1}},
		{"dns timeout is dns", &StageError{StageDNS, &net.DNSError{IsTimeout: true}}, Failure{StageDNS, -1}},
		{"dns stage deadline is dns", &StageError{StageDNS, context.DeadlineExceeded}, Failure{StageDNS, -1}},
		{"refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, Failure{StageTCP, -1}},
		{"dns not found", &net.DNSError{IsNotFound: true}, Failure{StageDNS, -1}},
		{"chain", &StageError{StageTLSHandshake, &ChainError{Err: x509.UnknownAuthorityError{}}}, Failure{StageTLSVerifyChain, -1}},
		{"x509 unknown authority", &StageError{StageTLSHandshake, x509.UnknownAuthorityError{}}, Failure{StageTLSVerifyChain, -1}},
		{"x509 hostname", &StageError{StageTLSHandshake, x509.HostnameError{}}, Failure{StageTLSVerifyHostname, -1}},
		{"hostname mismatch", &HostnameMismatchError{Name: "a", Mode: "san"}, Failure{StageTLSVerifyHostname, -1}},
		{"remote alert", &StageError{StageTLSHandshake, &net.OpError{Op: "remote error"}}, Failure{StageTLSAlert, -1}},
		{"connack v5", &ConnackError{135}, Failure{StageConnack, 135}},
		{"connack v3 sentinel", fmt.Errorf("x: %w", v3packets.ErrorRefusedNotAuthorised), Failure{StageConnack, 5}},
		{"handshake stage", &StageError{StageTLSHandshake, errors.New("eof")}, Failure{StageTLSHandshake, -1}},
		{"websocket stage", &StageError{StageWebsocket, errors.New("bad status")}, Failure{StageWebsocket, -1}},
		{"net timeout", &net.OpError{Op: "read", Err: &net.DNSError{IsTimeout: true}}, Failure{StageTimeout, -1}},
		{"other", errors.New("boom"), Failure{StageOther, -1}},
		{"nil", nil, Failure{StageOther, -1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.err); got != c.want {
				t.Fatalf("Classify(%v) = %+v, want %+v", c.err, got, c.want)
			}
		})
	}
}

func TestClassifyTimeoutWins(t *testing.T) {
	err := fmt.Errorf("wrap: %w", &StageError{StageTCP, context.DeadlineExceeded})
	if got := Classify(err); got.Stage != StageTimeout {
		t.Fatalf("got %+v", got)
	}
}

func TestClassifyDNSTimeoutIsDNS(t *testing.T) {
	err := &StageError{StageDNS, &net.DNSError{IsTimeout: true}}
	if got := Classify(err); got.Stage != StageDNS {
		t.Fatalf("got %+v", got)
	}
}

var mqttVersions = []string{"3.1.1", "5"}

// classifyConnect connects once per MQTT version and checks the classified stage.
func classifyConnect(t *testing.T, b scenario.Broker, timeout time.Duration, want Failure) {
	t.Helper()
	for _, v := range mqttVersions {
		t.Run(v, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			b := b
			b.Version, b.CleanStart = v, true
			c, err := Connect(ctx, b, "classify", func([]byte) {})
			if err == nil {
				c.Close()
				t.Fatal("connect unexpectedly succeeded")
			}
			if got := Classify(err); got != want {
				t.Fatalf("Classify(%v) = %+v, want %+v", err, got, want)
			}
		})
	}
}

func TestConnectFailureTCPRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	classifyConnect(t, scenario.Broker{URL: "mqtt://" + addr}, 2*time.Second, Failure{StageTCP, -1})
}

func TestConnectFailureDNS(t *testing.T) {
	classifyConnect(t, scenario.Broker{URL: "mqtt://name.invalid:1883"}, 3*time.Second, Failure{StageDNS, -1})
}

func TestConnectFailureTimeout(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	b := scenario.Broker{URL: "mqtt://" + l.Addr().String(), ConnectTimeout: scenario.Duration{Duration: 200 * time.Millisecond}}
	classifyConnect(t, b, 5*time.Second, Failure{StageTimeout, -1})
}

func TestConnectFailureTLSHandshake(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("not tls at all\r\n"))
			_ = c.Close()
		}
	}()
	classifyConnect(t, scenario.Broker{URL: "mqtts://" + l.Addr().String()}, 2*time.Second, Failure{StageTLSHandshake, -1})
}

func TestConnectFailureTLSVerify(t *testing.T) {
	pki := newTestPKI(t)
	cert := pki.issue(t, cnLeaf(testServerName, testServerName))
	url := serveTLS(t, cert, false)
	// Untrusted: the default roots do not include the test CA.
	classifyConnect(t, scenario.Broker{URL: url, TLS: scenario.TLS{ServerName: testServerName}}, 2*time.Second, Failure{StageTLSVerifyChain, -1})
	// Trusted CA, wrong name.
	classifyConnect(t, scenario.Broker{URL: url, TLS: scenario.TLS{CAPEM: string(pki.pem), ServerName: "wrong.example.net"}}, 2*time.Second, Failure{StageTLSVerifyHostname, -1})
}

func TestClientCertRejectedIsTLSAlert(t *testing.T) {
	pki := newTestPKI(t)
	cert := pki.issue(t, cnLeaf(testServerName, testServerName))
	for _, max := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		t.Run(tls.VersionName(max), func(t *testing.T) {
			b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			_ = b.AddHook(new(auth.AllowHook), nil)
			pool := x509.NewCertPool()
			pool.AddCert(pki.cert)
			l := listeners.NewTCP(listeners.Config{ID: "mtls", Address: "127.0.0.1:0", TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, MaxVersion: max,
				ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool,
			}})
			if err := b.AddListener(l); err != nil {
				t.Fatal(err)
			}
			_ = b.Serve()
			t.Cleanup(func() { b.Close() })
			classifyConnect(t, scenario.Broker{URL: "mqtts://" + l.Address(), TLS: scenario.TLS{CAPEM: string(pki.pem), ServerName: testServerName}}, 2*time.Second, Failure{StageTLSAlert, -1})
		})
	}
}

type denyHook struct{ broker.HookBase }

func (denyHook) ID() string                                              { return "deny" }
func (denyHook) Provides(b byte) bool                                    { return b == broker.OnConnectAuthenticate }
func (denyHook) OnConnectAuthenticate(*broker.Client, mochi.Packet) bool { return false }

func TestConnackRefusalCode(t *testing.T) {
	b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	_ = b.AddHook(new(denyHook), nil)
	l := listeners.NewTCP(listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := b.AddListener(l); err != nil {
		t.Fatal(err)
	}
	_ = b.Serve()
	t.Cleanup(func() { b.Close() })
	for v, code := range map[string]int{"3.1.1": 5, "5": 134} { // mochi refuses with 0x86 (bad username or password), not 0x87
		t.Run(v, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			c, err := Connect(ctx, scenario.Broker{URL: "mqtt://" + l.Address(), Version: v, CleanStart: true}, "denied", func([]byte) {})
			if err == nil {
				c.Close()
				t.Fatal("connect unexpectedly succeeded")
			}
			if got := Classify(err); got != (Failure{StageConnack, code}) {
				t.Fatalf("Classify(%v) = %+v, want connack %d", err, got, code)
			}
		})
	}
}

func TestWebsocketUpgradeFailure(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(s.Close)
	classifyConnect(t, scenario.Broker{URL: "ws" + strings.TrimPrefix(s.URL, "http")}, 2*time.Second, Failure{StageWebsocket, -1})
}
