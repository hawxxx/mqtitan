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
	"net/netip"
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
	// Disconnect sends an MQTT DISCONNECT, then closes like Close.
	Disconnect()
	Done() <-chan struct{}
	// RemoteAddr is the unmapped broker address the connection was made to.
	RemoteAddr() netip.AddrPort
}

// TLSConfig builds the client TLS configuration. With strict set (the
// scenario's safety.strictSecrets) a key file with findings is refused.
func TLSConfig(s scenario.TLS, strict bool) (*tls.Config, error) {
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
		if err := checkKeyFile(s.KeyFile, strict); err != nil {
			return nil, err
		}
		cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
		if err != nil {
			return nil, errors.New("TLS client certificate or key cannot be loaded")
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
	dialer *dialer
}

func NewConnector(b scenario.Broker) (*Connector, error) {
	return NewConnectorWithHooks(b, Hooks{})
}

// NewConnectorWithHooks is NewConnector with dial observers.
func NewConnectorWithHooks(b scenario.Broker, h Hooks) (*Connector, error) {
	u, err := url.Parse(b.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("invalid broker URL")
	}
	cfg, err := TLSConfig(b.TLS, h.StrictSecrets)
	if err != nil {
		return nil, err
	}
	if cfg.ServerName == "" {
		cfg.ServerName = u.Hostname()
	}
	if mode := b.TLS.HostnameVerification; mode == "cn-fallback" || mode == "none" {
		roots := cfg.RootCAs
		if roots == nil {
			if roots, err = x509.SystemCertPool(); err != nil {
				return nil, errors.New("TLS system trust roots unavailable")
			}
		}
		cfg.InsecureSkipVerify = true // chain and name are verified by VerifyConnection
		cfg.VerifyConnection = verifyPeer(roots, mode, cfg.ServerName)
	}
	return &Connector{b, u, cfg, newDialer(u.Hostname(), portOf(u), b.DNS, h)}, nil
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
		return connect5(ctx, b, u, cfg, f.dialer, id, receive)
	}
	return connect3(ctx, b, u, cfg, f.dialer, id, receive)
}

// portOf returns the URL port or the scheme default.
func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "mqtts":
		return "8883"
	case "ws":
		return "80"
	case "wss":
		return "443"
	}
	return "1883"
}
func dial(ctx context.Context, u *url.URL, cfg *tls.Config, d *dialer) (net.Conn, *alertConn, netip.AddrPort, error) {
	ws := u.Scheme == "ws" || u.Scheme == "wss"
	conn, remote, err := d.dialTCP(ctx)
	if err != nil {
		return nil, nil, netip.AddrPort{}, err
	}
	if u.Scheme == "mqtts" || u.Scheme == "wss" {
		tc := tls.Client(conn, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, nil, remote, &StageError{StageTLSHandshake, err}
		}
		conn = tc
	}
	ac := &alertConn{Conn: conn}
	if !ws {
		return ac, ac, remote, nil
	}
	config, err := websocket.NewConfig(u.String(), "http://localhost")
	if err != nil {
		_ = conn.Close()
		return nil, nil, remote, &StageError{StageWebsocket, err}
	}
	config.Protocol = []string{"mqtt"}
	config.TlsConfig = cfg
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(deadline(ctx))
	wsConn, err := websocket.NewClient(config, ac)
	if err != nil {
		_ = conn.Close()
		return nil, nil, remote, &StageError{StageWebsocket, err}
	}
	_ = conn.SetDeadline(time.Time{})
	wsConn.PayloadType = websocket.BinaryFrame
	return wsConn, ac, remote, nil
}

type client3 struct {
	c      v3.Client
	conn   net.Conn
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	remote netip.AddrPort
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

func connect3(ctx context.Context, b scenario.Broker, u *url.URL, cfg *tls.Config, d *dialer, id string, receive func([]byte)) (Client, error) {
	c := &client3{done: make(chan struct{})}
	o := v3.NewClientOptions().AddBroker(b.URL).SetClientID(id).SetProtocolVersion(4).SetCleanSession(b.CleanStart).SetAutoReconnect(false).SetConnectRetry(false).SetConnectTimeout(time.Until(deadline(ctx))).SetWriteTimeout(10 * time.Second).SetTLSConfig(cfg).SetOrderMatters(true)
	if b.KeepAlive.Duration > 0 {
		o.SetKeepAlive(time.Duration(keepAliveSeconds(b.KeepAlive.Duration)) * time.Second)
	}
	o.SetUsername(b.Username).SetPassword(b.Password)
	o.SetDefaultPublishHandler(func(_ v3.Client, m v3.Message) { receive(m.Payload()) })
	o.SetConnectionLostHandler(func(_ v3.Client, _ error) { c.once.Do(func() { close(c.done) }) })
	var alert atomic.Pointer[alertConn]
	var dialErr atomic.Pointer[error]
	o.SetCustomOpenConnectionFn(func(_ *url.URL, _ v3.ClientOptions) (net.Conn, error) {
		conn, ac, remote, err := dial(ctx, u, cfg, d)
		if err != nil {
			dialErr.Store(&err)
			return nil, err
		}
		alert.Store(ac)
		conn = &reportClosedConn{Conn: conn}
		c.mu.Lock()
		c.conn = conn
		c.remote = remote
		c.mu.Unlock()
		return conn, nil
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
		err = alert.Load().alertOr(err)
		c.closeConn()
		// A failed dial carries its stage; a resolver timeout must stay a DNS failure.
		if p := dialErr.Load(); p != nil {
			var se *StageError
			if errors.As(*p, &se) && se.Stage == StageDNS {
				return nil, *p
			}
		}
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

// keepAliveSeconds rounds up, so a sub-second keepAlive never becomes 0 (disabled).
func keepAliveSeconds(d time.Duration) uint16 {
	return uint16(min(65535, (d+time.Second-1)/time.Second))
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
func (c *client3) Disconnect() {
	// A clean disconnect does not invoke the connection-lost handler, so
	// release Done ourselves once paho has joined its workers.
	c.c.Disconnect(250)
	c.closeConn()
	c.once.Do(func() { close(c.done) })
}
func (c *client3) RemoteAddr() netip.AddrPort {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remote
}
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
	token := c.c.Subscribe(topic, qos, nil)
	if err := wait(ctx, token); err != nil {
		return err
	}
	// MQTT 3.1.1 reports a refused filter with return code 0x80, not a token error.
	if st, ok := token.(*v3.SubscribeToken); ok {
		if code, found := st.Result()[topic]; found && code >= 0x80 {
			return fmt.Errorf("SUBACK reason %d", code)
		}
	}
	return nil
}

type client5 struct {
	c      *paho.Client
	conn   net.Conn
	remote netip.AddrPort
}

func connect5(ctx context.Context, b scenario.Broker, u *url.URL, cfg *tls.Config, d *dialer, id string, receive func([]byte)) (Client, error) {
	conn, alert, remote, err := dial(ctx, u, cfg, d)
	if err != nil {
		return nil, err
	}
	conn = packets.NewThreadSafeConn(conn)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	c := paho.NewClient(paho.ClientConfig{Conn: conn, ClientID: id, PacketTimeout: packetTimeout(b), OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(p paho.PublishReceived) (bool, error) { receive(p.Packet.Payload); return true, nil }}})
	keep := uint16(30)
	if b.KeepAlive.Duration > 0 {
		keep = keepAliveSeconds(b.KeepAlive.Duration)
	}
	ack, err := c.Connect(ctx, &paho.Connect{ClientID: id, CleanStart: b.CleanStart, KeepAlive: keep, Username: b.Username, Password: []byte(b.Password), UsernameFlag: b.Username != "", PasswordFlag: b.Password != ""})
	// paho.golang returns a non-nil Connack together with an error for a refusal.
	if ack != nil && ack.ReasonCode >= 0x80 {
		_ = conn.Close()
		return nil, &ConnackError{ack.ReasonCode}
	}
	if err != nil {
		err = alert.alertOr(err)
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return &client5{c: c, conn: conn, remote: remote}, nil
}
func (c *client5) Close()                     { _ = c.conn.Close(); <-c.c.Done() }
func (c *client5) Done() <-chan struct{}      { return c.c.Done() }
func (c *client5) RemoteAddr() netip.AddrPort { return c.remote }
func (c *client5) Disconnect() {
	// paho writes DISCONNECT without a deadline; bound it so a broker that
	// stopped reading cannot block the session (Close then unblocks).
	_ = c.conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
	_ = c.c.Disconnect(&paho.Disconnect{ReasonCode: 0})
	c.Close()
}
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

// packetTimeout bounds paho.golang's CONNECT, SUBSCRIBE and QoS 1/2 PUBLISH
// waits. The library caps each context deadline at this value, so it must
// not be shorter than the scenario's connect and operation timeouts.
func packetTimeout(b scenario.Broker) time.Duration {
	return max(10*time.Second, b.ConnectTimeout.Duration, b.OperationTimeout.Duration)
}
