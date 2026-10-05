package mqtt

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	broker "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"golang.org/x/net/websocket"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebsocketDelivery(t *testing.T) {
	for _, v := range []string{"3.1.1", "5"} {
		t.Run(v, func(t *testing.T) {
			b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			_ = b.AddHook(new(auth.AllowHook), nil)
			_ = b.Serve()
			defer b.Close()
			server := httptest.NewServer(websocket.Handler(func(c *websocket.Conn) { c.PayloadType = websocket.BinaryFrame; _ = b.EstablishConnection("ws", c) }))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			received := make(chan string, 1)
			cfg := scenario.Broker{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Version: v, CleanStart: true}
			c, err := Connect(ctx, cfg, "ws-test", func(data []byte) { received <- string(data) })
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err = c.Subscribe(ctx, "ws", 1); err != nil {
				t.Fatal(err)
			}
			if err = c.Publish(ctx, "ws", 1, false, []byte("websocket works")); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-received:
				if got != "websocket works" {
					t.Fatalf("%q", got)
				}
			case <-ctx.Done():
				t.Fatal("no delivery")
			}
		})
	}
}

func TestConnectorReusesParsedTrustRoots(t *testing.T) {
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert := fixture.TLS.Certificates[0]
	fixture.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	_ = b.AddHook(new(auth.AllowHook), nil)
	l := listeners.NewTCP(listeners.Config{ID: "cached-tls", Address: "127.0.0.1:0", TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}})
	if err := b.AddListener(l); err != nil {
		t.Fatal(err)
	}
	_ = b.Serve()
	defer b.Close()
	factory, err := NewConnector(scenario.Broker{URL: "mqtts://" + l.Address(), Version: "5", TLS: scenario.TLS{CAFile: ca, ServerName: "example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(ca); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := factory.Connect(ctx, "cached-ca", func([]byte) {})
	if err != nil {
		t.Fatal("trust roots re-read per connection:", err)
	}
	c.Close()
}

func TestTLSCAAndServerNameVerification(t *testing.T) {
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert := fixture.TLS.Certificates[0]
	fixture.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	_ = b.AddHook(new(auth.AllowHook), nil)
	l := listeners.NewTCP(listeners.Config{ID: "tls", Address: "127.0.0.1:0", TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}})
	if err := b.AddListener(l); err != nil {
		t.Fatal(err)
	}
	_ = b.Serve()
	defer b.Close()
	for _, v := range []string{"3.1.1", "5"} {
		t.Run(v, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			cfg := scenario.Broker{URL: "mqtts://" + l.Address(), Version: v, CleanStart: true, TLS: scenario.TLS{CAFile: ca, ServerName: "example.com"}}
			c, err := Connect(ctx, cfg, "trusted", func([]byte) {})
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
			cfg.TLS.ServerName = "wrong.example"
			if c, err = Connect(ctx, cfg, "wrong-sni", func([]byte) {}); err == nil {
				c.Close()
				t.Fatal("wrong SNI accepted")
			}
			cfg.TLS = scenario.TLS{}
			if c, err = Connect(ctx, cfg, "untrusted", func([]byte) {}); err == nil {
				c.Close()
				t.Fatal("untrusted server accepted")
			}
		})
	}
}

func TestCloseJoinsReceiptCallback(t *testing.T) {
	b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	_ = b.AddHook(new(auth.AllowHook), nil)
	l := listeners.NewTCP(listeners.Config{ID: "tcp", Address: "127.0.0.1:0"})
	if err := b.AddListener(l); err != nil {
		t.Fatal(err)
	}
	_ = b.Serve()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	cfg := scenario.Broker{URL: "mqtt://" + l.Address(), Version: "3.1.1", CleanStart: true}
	sub, err := Connect(ctx, cfg, "sub", func([]byte) { close(entered); <-release })
	if err != nil {
		t.Fatal(err)
	}
	pub, err := Connect(ctx, cfg, "pub", func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	if err = sub.Subscribe(ctx, "join", 0); err != nil {
		t.Fatal(err)
	}
	_ = pub.Publish(ctx, "join", 0, false, []byte("join"))
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("no callback")
	}
	closed := make(chan struct{})
	go func() { sub.Close(); close(closed) }()
	select {
	case <-closed:
		close(release)
		t.Fatal("Close returned while receipt callback was running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("Close did not finish")
	}
}
func TestCancelledHandshakeClosesSocket(t *testing.T) {
	for _, v := range []string{"3.1.1", "5"} {
		t.Run(v, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				_, _ = io.Copy(io.Discard, c)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err = Connect(ctx, scenario.Broker{URL: "mqtt://" + l.Addr().String(), Version: v, ConnectTimeout: scenario.Duration{Duration: time.Second}}, "stall", func([]byte) {})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%v", err)
			}
			if time.Since(start) > 300*time.Millisecond {
				t.Fatal("cancellation waited for configured connect timeout")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("socket leaked")
			}
		})
	}
}

func TestPublishTimeoutInterruptsBlockedSocket(t *testing.T) {
	for _, v := range []string{"3.1.1", "5"} {
		t.Run(v, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			release := make(chan struct{})
			defer close(release)
			go func() {
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				buf := make([]byte, 4096)
				_, _ = c.Read(buf)
				ack := []byte{0x20, 2, 0, 0}
				if v == "5" {
					ack = []byte{0x20, 3, 0, 0, 0}
				}
				_, _ = c.Write(ack)
				<-release
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c, err := Connect(ctx, scenario.Broker{URL: "mqtt://" + l.Addr().String(), Version: v, CleanStart: true}, "blocked", func([]byte) {})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			op, stop := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer stop()
			start := time.Now()
			err = c.Publish(op, "blocked", 0, false, make([]byte, 8*1024*1024))
			if err == nil {
				t.Fatal("publish unexpectedly succeeded")
			}
			if time.Since(start) > 300*time.Millisecond {
				t.Fatal("publish exceeded operation timeout")
			}
		})
	}
}
