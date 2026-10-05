// Package mqtt adapts the Eclipse Paho MQTT clients to a context-aware lifecycle.
package mqtt

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"
	v3 "github.com/eclipse/paho.mqtt.golang"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"golang.org/x/net/websocket"
	"net"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type Client interface {
	Publish(context.Context, string, byte, bool, []byte) error
	Subscribe(context.Context, string, byte) error
	Close()
	Done() <-chan struct{}
}

func TLSConfig(s scenario.TLS) (*tls.Config, error) {
	if s.Profile != "" {
		return nil, errors.New("TLS certificate profile requires its controller; use --controller or provide local PEM files")
	}
	c := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: s.ServerName, InsecureSkipVerify: s.InsecureSkipVerify}
	if s.CAFile != "" || s.CAPEM != "" {
		data := []byte(s.CAPEM)
		if s.CAFile != "" {
			var err error
			data, err = os.ReadFile(s.CAFile)
			if err != nil {
				return nil, errors.New("TLS CA file cannot be read")
			}
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(data) {
			return nil, errors.New("TLS CA file contains no certificates")
		}
		c.RootCAs = pool
	}
	if s.CertFile != "" || s.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("TLS client certificate: %w", err)
		}
		c.Certificates = []tls.Certificate{cert}
	}
	if s.CertPEM != "" || s.KeyPEM != "" {
		cert, err := tls.X509KeyPair([]byte(s.CertPEM), []byte(s.KeyPEM))
		if err != nil {
			return nil, errors.New("TLS client certificate and private key are invalid or do not match")
		}
		c.Certificates = []tls.Certificate{cert}
	}
	return c, nil
}

type Connector struct {
	broker scenario.Broker
	url    *url.URL
	tls    *tls.Config
}

func NewConnector(b scenario.Broker) (*Connector, error) {
	u, err := url.Parse(b.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("invalid broker URL")
	}
	cfg, err := TLSConfig(b.TLS)
	if err != nil {
		return nil, err
	}
	if cfg.ServerName == "" {
		cfg.ServerName = u.Hostname()
	}
	return &Connector{b, u, cfg}, nil
}
func Connect(parent context.Context, b scenario.Broker, id string, receive func([]byte)) (Client, error) {
	factory, err := NewConnector(b)
	if err != nil {
		return nil, err
	}
	return factory.Connect(parent, id, receive)
}
func (f *Connector) Connect(parent context.Context, id string, receive func([]byte)) (Client, error) {
	b := f.broker
	timeout := b.ConnectTimeout.Duration
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	u, cfg := f.url, f.tls
	if b.Version == "5" || b.Version == "5.0" {
		return connect5(ctx, b, u, cfg, id, receive)
	}
	return connect3(ctx, b, u, cfg, id, receive)
}
func dial(ctx context.Context, u *url.URL, cfg *tls.Config) (net.Conn, error) {
	if u.Scheme == "ws" || u.Scheme == "wss" {
		config, err := websocket.NewConfig(u.String(), "http://localhost")
		if err != nil {
			return nil, err
		}
		config.Protocol = []string{"mqtt"}
		config.TlsConfig = cfg
		host := u.Host
		if u.Port() == "" {
			port := "80"
			if u.Scheme == "wss" {
				port = "443"
			}
			host = net.JoinHostPort(u.Hostname(), port)
		}
		d := &net.Dialer{}
		var conn net.Conn
		if u.Scheme == "wss" {
			conn, err = (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(ctx, "tcp", host)
		} else {
			conn, err = d.DialContext(ctx, "tcp", host)
		}
		if err != nil {
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		_ = conn.SetDeadline(deadline(ctx))
		ws, err := websocket.NewClient(config, conn)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		_ = conn.SetDeadline(time.Time{})
		ws.PayloadType = websocket.BinaryFrame
		return ws, nil
	}
	host := u.Host
	if u.Port() == "" {
		port := "1883"
		if u.Scheme == "mqtts" {
			port = "8883"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	d := &net.Dialer{}
	if u.Scheme == "mqtts" {
		return (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(ctx, "tcp", host)
	}
	return d.DialContext(ctx, "tcp", host)
}

type client3 struct {
	c    v3.Client
	conn net.Conn
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
}

// Paho suppresses the standard local socket-close error. Report a distinct
// read error so its connection-lost path joins every communication worker.
type reportClosedConn struct {
	net.Conn
	closed atomic.Bool
}
type readError struct{ cause error }

func (e readError) Error() string { return "MQTT transport read interrupted" }
func (e readError) Unwrap() error { return e.cause }
func (c *reportClosedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil && c.closed.Load() {
		return n, readError{err}
	}
	return n, err
}
func (c *reportClosedConn) Close() error { c.closed.Store(true); return c.Conn.Close() }

func connect3(ctx context.Context, b scenario.Broker, u *url.URL, cfg *tls.Config, id string, receive func([]byte)) (Client, error) {
	c := &client3{done: make(chan struct{})}
	o := v3.NewClientOptions().AddBroker(b.URL).SetClientID(id).SetProtocolVersion(4).SetCleanSession(b.CleanStart).SetAutoReconnect(false).SetConnectRetry(false).SetConnectTimeout(time.Until(deadline(ctx))).SetWriteTimeout(10 * time.Second).SetTLSConfig(cfg).SetOrderMatters(true)
	if b.KeepAlive.Duration > 0 {
		o.SetKeepAlive(b.KeepAlive.Duration)
	}
	o.SetUsername(b.Username).SetPassword(b.Password)
	o.SetDefaultPublishHandler(func(_ v3.Client, m v3.Message) { receive(m.Payload()) })
	o.SetConnectionLostHandler(func(_ v3.Client, _ error) { c.once.Do(func() { close(c.done) }) })
	o.SetCustomOpenConnectionFn(func(_ *url.URL, _ v3.ClientOptions) (net.Conn, error) {
		conn, err := dial(ctx, u, cfg)
		if err == nil {
			conn = &reportClosedConn{Conn: conn}
			c.mu.Lock()
			c.conn = conn
			c.mu.Unlock()
		}
		return conn, err
	})
	c.c = v3.NewClient(o)
	token := c.c.Connect()
	select {
	case <-ctx.Done():
		c.closeConn()
		<-token.Done()
		c.c.Disconnect(0)
		return nil, ctx.Err()
	case <-token.Done():
	}
	if err := token.Error(); err != nil {
		c.closeConn()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !time.Now().Before(deadline(ctx)) {
			return nil, context.DeadlineExceeded
		}
		return nil, err
	}
	if ctx.Err() != nil {
		c.Close()
		return nil, ctx.Err()
	}
	return c, nil
}
func deadline(ctx context.Context) time.Time { d, _ := ctx.Deadline(); return d }
func (c *client3) closeConn() {
	c.mu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.mu.Unlock()
}

// Paho reports connection loss after its network and routing workers have joined.
func (c *client3) Close()                { c.closeConn(); <-c.done }
func (c *client3) Done() <-chan struct{} { return c.done }
func wait(ctx context.Context, t v3.Token) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.Done():
		return t.Error()
	}
}
func (c *client3) Publish(ctx context.Context, topic string, qos byte, retain bool, data []byte) error {
	stop := context.AfterFunc(ctx, c.closeConn)
	defer stop()
	return wait(ctx, c.c.Publish(topic, qos, retain, data))
}
func (c *client3) Subscribe(ctx context.Context, topic string, qos byte) error {
	stop := context.AfterFunc(ctx, c.closeConn)
	defer stop()
	return wait(ctx, c.c.Subscribe(topic, qos, nil))
}

type client5 struct {
	c    *paho.Client
	conn net.Conn
}

func connect5(ctx context.Context, b scenario.Broker, u *url.URL, cfg *tls.Config, id string, receive func([]byte)) (Client, error) {
	conn, err := dial(ctx, u, cfg)
	if err != nil {
		return nil, err
	}
	conn = packets.NewThreadSafeConn(conn)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	c := paho.NewClient(paho.ClientConfig{Conn: conn, ClientID: id, PacketTimeout: 10 * time.Second, OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(p paho.PublishReceived) (bool, error) { receive(p.Packet.Payload); return true, nil }}})
	keep := uint16(30)
	if b.KeepAlive.Duration > 0 {
		keep = uint16(b.KeepAlive.Duration / time.Second)
	}
	ack, err := c.Connect(ctx, &paho.Connect{ClientID: id, CleanStart: b.CleanStart, KeepAlive: keep, Username: b.Username, Password: []byte(b.Password), UsernameFlag: b.Username != "", PasswordFlag: b.Password != ""})
	if err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if ack.ReasonCode >= 128 {
		_ = conn.Close()
		return nil, fmt.Errorf("CONNACK reason %d", ack.ReasonCode)
	}
	return &client5{c: c, conn: conn}, nil
}
func (c *client5) Close()                { _ = c.conn.Close(); <-c.c.Done() }
func (c *client5) Done() <-chan struct{} { return c.c.Done() }
func (c *client5) Publish(ctx context.Context, topic string, qos byte, retain bool, data []byte) error {
	stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() })
	defer stop()
	r, err := c.c.Publish(ctx, &paho.Publish{Topic: topic, QoS: qos, Retain: retain, Payload: data})
	if err == nil && r != nil && r.ReasonCode >= 128 {
		return fmt.Errorf("PUBACK reason %d", r.ReasonCode)
	}
	return err
}
func (c *client5) Subscribe(ctx context.Context, topic string, qos byte) error {
	stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() })
	defer stop()
	r, err := c.c.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: topic, QoS: qos}}})
	if err != nil {
		return err
	}
	for _, code := range r.Reasons {
		if code >= 128 {
			return fmt.Errorf("SUBACK reason %d", code)
		}
	}
	return nil
}
