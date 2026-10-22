package mqtt

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	broker "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"golang.org/x/net/dns/dnsmessage"
)

const (
	dnsAnswer = iota
	dnsDrop
	dnsServfail
	dnsNXDomain
)

type dnsQuery struct {
	server int
	name   string
	typ    dnsmessage.Type
}

type queryLog struct {
	mu sync.Mutex
	q  []dnsQuery
}

func (l *queryLog) add(q dnsQuery) { l.mu.Lock(); l.q = append(l.q, q); l.mu.Unlock() }
func (l *queryLog) all() []dnsQuery {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]dnsQuery(nil), l.q...)
}
func (l *queryLog) count(server int, typ dnsmessage.Type) (n int) {
	for _, q := range l.all() {
		if (server < 0 || q.server == server) && (typ == 0 || q.typ == typ) {
			n++
		}
	}
	return n
}

type fakeDNS struct {
	id      int
	conn    net.PacketConn
	log     *queryLog
	mode    atomic.Int32
	mu      sync.Mutex
	answers []netip.Addr
}

func (f *fakeDNS) addr() netip.AddrPort { return f.conn.LocalAddr().(*net.UDPAddr).AddrPort() }
func (f *fakeDNS) str() string          { return f.addr().String() }
func (f *fakeDNS) setAnswers(a ...netip.Addr) {
	f.mu.Lock()
	f.answers = a
	f.mu.Unlock()
}

func startDNS(t *testing.T, log *queryLog, id int, answers ...string) *fakeDNS {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDNS{id: id, conn: conn, log: log}
	for _, a := range answers {
		f.answers = append(f.answers, netip.MustParseAddr(a))
	}
	t.Cleanup(func() { _ = conn.Close() })
	go f.serve()
	return f
}

func (f *fakeDNS) serve() {
	buf := make([]byte, 1500)
	for {
		n, from, err := f.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		var p dnsmessage.Parser
		h, err := p.Start(buf[:n])
		if err != nil {
			continue
		}
		q, err := p.Question()
		if err != nil {
			continue
		}
		f.log.add(dnsQuery{f.id, q.Name.String(), q.Type})
		mode := f.mode.Load()
		if mode == dnsDrop {
			continue
		}
		rcode := dnsmessage.RCodeSuccess
		switch mode {
		case dnsServfail:
			rcode = dnsmessage.RCodeServerFailure
		case dnsNXDomain:
			rcode = dnsmessage.RCodeNameError
		}
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RCode: rcode, RecursionDesired: h.RecursionDesired, RecursionAvailable: true})
		b.EnableCompression()
		_ = b.StartQuestions()
		_ = b.Question(q)
		_ = b.StartAnswers()
		if mode == dnsAnswer {
			f.mu.Lock()
			answers := f.answers
			f.mu.Unlock()
			for _, a := range answers {
				hdr := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 1}
				switch {
				case q.Type == dnsmessage.TypeA && a.Is4():
					hdr.Type = dnsmessage.TypeA
					_ = b.AResource(hdr, dnsmessage.AResource{A: a.As4()})
				case q.Type == dnsmessage.TypeAAAA && a.Is6():
					hdr.Type = dnsmessage.TypeAAAA
					_ = b.AAAAResource(hdr, dnsmessage.AAAAResource{AAAA: a.As16()})
				}
			}
		}
		out, err := b.Finish()
		if err == nil {
			_, _ = f.conn.WriteTo(out, from)
		}
	}
}

// tcpSink accepts and closes connections on ip:port (port 0 picks one).
func tcpSink(t *testing.T, ip string, port int) (netip.AddrPort, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			_ = c.Close()
		}
	}()
	return l.Addr().(*net.TCPAddr).AddrPort(), &n
}

type hookLog struct {
	mu       sync.Mutex
	resolved [][]netip.Addr
	errs     []netip.AddrPort
	conns    []netip.AddrPort
}

func (h *hookLog) hooks() Hooks {
	return Hooks{
		OnResolve: func(a []netip.Addr) {
			h.mu.Lock()
			h.resolved = append(h.resolved, append([]netip.Addr(nil), a...))
			h.mu.Unlock()
		},
		OnResolveError: func(r netip.AddrPort) { h.mu.Lock(); h.errs = append(h.errs, r); h.mu.Unlock() },
		OnConnect:      func(r netip.AddrPort) { h.mu.Lock(); h.conns = append(h.conns, r); h.mu.Unlock() },
	}
}

func shortAttempt(t *testing.T) {
	old := resolveAttemptTimeout
	resolveAttemptTimeout = 300 * time.Millisecond
	t.Cleanup(func() { resolveAttemptTimeout = old })
}

func testBroker(raw string, d *scenario.DNS) scenario.Broker {
	return scenario.Broker{URL: raw, Version: "3.1.1", ConnectTimeout: scenario.Duration{Duration: 5 * time.Second}, DNS: d}
}

func dialOnce(t *testing.T, f *Connector) (net.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, _, err := dial(ctx, f.url, f.tls, f.dialer)
	if err == nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, err
}

func servers(d ...*fakeDNS) []string {
	out := make([]string, len(d))
	for i, s := range d {
		out[i] = s.str()
	}
	return out
}

func TestResolverRoundRobin(t *testing.T) {
	log := &queryLog{}
	s0, s1 := startDNS(t, log, 0, "127.0.0.1"), startDNS(t, log, 1, "127.0.0.1")
	b := testBroker("mqtt://broker.test:1883", &scenario.DNS{Resolvers: servers(s0, s1), Family: "ipv4", ResolveEachDial: true})
	f, err := NewConnectorWithHooks(b, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if _, err := f.dialer.lookup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if log.count(0, dnsmessage.TypeA) != 2 || log.count(1, dnsmessage.TypeA) != 2 || log.count(-1, dnsmessage.TypeAAAA) != 0 {
		t.Fatalf("queries = %+v", log.all())
	}
	for i, q := range log.all() {
		if q.server != i%2 {
			t.Fatalf("query %d went to server %d: %+v", i, q.server, log.all())
		}
	}
}

func TestResolverFailover(t *testing.T) {
	shortAttempt(t)
	remote, _ := tcpSink(t, "127.0.0.1", 0)
	raw := fmt.Sprintf("mqtt://broker.test:%d", remote.Port())
	for _, mode := range []int32{dnsServfail, dnsDrop} {
		t.Run(fmt.Sprint("first fails ", mode), func(t *testing.T) {
			log := &queryLog{}
			s0, s1 := startDNS(t, log, 0, "127.0.0.1"), startDNS(t, log, 1, "127.0.0.1")
			s0.mode.Store(mode)
			var h hookLog
			f, err := NewConnectorWithHooks(testBroker(raw, &scenario.DNS{Resolvers: servers(s0, s1), Family: "ipv4"}), h.hooks())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := dialOnce(t, f); err != nil {
				t.Fatal(err)
			}
			if len(h.errs) != 1 || h.errs[0] != s0.addr() {
				t.Fatalf("resolve errors = %v, want [%v]", h.errs, s0.addr())
			}
			if len(h.conns) != 1 {
				t.Fatalf("connects = %v", h.conns)
			}
		})
		t.Run(fmt.Sprint("both fail ", mode), func(t *testing.T) {
			log := &queryLog{}
			s0, s1 := startDNS(t, log, 0, "127.0.0.1"), startDNS(t, log, 1, "127.0.0.1")
			s0.mode.Store(mode)
			s1.mode.Store(mode)
			var h hookLog
			f, err := NewConnectorWithHooks(testBroker(raw, &scenario.DNS{Resolvers: servers(s0, s1), Family: "ipv4"}), h.hooks())
			if err != nil {
				t.Fatal(err)
			}
			_, err = dialOnce(t, f)
			if err == nil {
				t.Fatal("dial succeeded")
			}
			if got := Classify(err); got.Stage != StageDNS {
				t.Fatalf("Classify = %v (%v), want dns", got, err)
			}
			if len(h.errs) != 2 {
				t.Fatalf("resolve errors = %v", h.errs)
			}
		})
	}
	t.Run("nxdomain stops", func(t *testing.T) {
		log := &queryLog{}
		s0, s1 := startDNS(t, log, 0, "127.0.0.1"), startDNS(t, log, 1, "127.0.0.1")
		s0.mode.Store(dnsNXDomain)
		f, err := NewConnectorWithHooks(testBroker(raw, &scenario.DNS{Resolvers: servers(s0, s1), Family: "ipv4"}), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = dialOnce(t, f)
		if err == nil || Classify(err).Stage != StageDNS {
			t.Fatalf("err = %v", err)
		}
		if n := log.count(1, 0); n != 0 {
			t.Fatalf("second resolver received %d queries", n)
		}
	})
}

func TestResolveEachDialNoCache(t *testing.T) {
	remote, _ := tcpSink(t, "127.0.0.1", 0)
	raw := fmt.Sprintf("mqtt://broker.test:%d", remote.Port())
	for _, each := range []bool{true, false} {
		log := &queryLog{}
		s := startDNS(t, log, 0, "127.0.0.1")
		f, err := NewConnectorWithHooks(testBroker(raw, &scenario.DNS{Resolvers: servers(s), Family: "ipv4", ResolveEachDial: each}), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if _, err := dialOnce(t, f); err != nil {
				t.Fatal(err)
			}
		}
		want := 1
		if each {
			want = 3
		}
		if got := log.count(-1, dnsmessage.TypeA); got != want {
			t.Fatalf("resolveEachDial=%v: %d lookups, want %d", each, got, want)
		}
	}
}

func twoSinks(t *testing.T) (a, b netip.AddrPort, na, nb *atomic.Int32) {
	for range 20 {
		a, na = tcpSink(t, "127.0.0.2", 0)
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.3", strconv.Itoa(int(a.Port()))))
		if err != nil {
			continue
		}
		_ = l.Close()
		b, nb = tcpSink(t, "127.0.0.3", int(a.Port()))
		return
	}
	t.Skip("cannot bind matching loopback ports")
	return
}

func TestPickFirstOrder(t *testing.T) {
	a, _, na, nb := twoSinks(t)
	log := &queryLog{}
	s := startDNS(t, log, 0, "127.0.0.2", "127.0.0.3")
	f, err := NewConnectorWithHooks(testBroker(fmt.Sprintf("mqtt://broker.test:%d", a.Port()), &scenario.DNS{Resolvers: servers(s), Family: "ipv4", Pick: "first"}), Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if _, err := dialOnce(t, f); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if na.Load() != 20 || nb.Load() != 0 {
		t.Fatalf("connections .2=%d .3=%d", na.Load(), nb.Load())
	}
}

func TestPickRandomCoversAll(t *testing.T) {
	a, _, na, nb := twoSinks(t)
	log := &queryLog{}
	s := startDNS(t, log, 0, "127.0.0.2", "127.0.0.3")
	h := Hooks{Seed: 7}
	f, err := NewConnectorWithHooks(testBroker(fmt.Sprintf("mqtt://broker.test:%d", a.Port()), &scenario.DNS{Resolvers: servers(s), Family: "ipv4", Pick: "random"}), h)
	if err != nil {
		t.Fatal(err)
	}
	for range 40 {
		if _, err := dialOnce(t, f); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if na.Load() == 0 || nb.Load() == 0 {
		t.Fatalf("connections .2=%d .3=%d", na.Load(), nb.Load())
	}
}

func TestFamilyFilter(t *testing.T) {
	log := &queryLog{}
	s := startDNS(t, log, 0, "::1", "127.0.0.1")
	for _, tt := range []struct {
		family string
		want   func(netip.Addr) bool
		n      int
	}{
		{"ipv4", func(a netip.Addr) bool { return a.Is4() }, 1},
		{"ipv6", func(a netip.Addr) bool { return a.Is6() && !a.Is4In6() }, 1},
		{"any", func(netip.Addr) bool { return true }, 2},
	} {
		var h hookLog
		f, err := NewConnectorWithHooks(testBroker("mqtt://broker.test:1883", &scenario.DNS{Resolvers: servers(s), Family: tt.family}), h.hooks())
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.dialer.lookup(context.Background())
		if err != nil {
			t.Fatal(tt.family, err)
		}
		if len(got) != tt.n || len(h.resolved) != 1 || len(h.resolved[0]) != tt.n {
			t.Fatalf("%s: answers %v, hook %v", tt.family, got, h.resolved)
		}
		for _, a := range got {
			if !tt.want(a) {
				t.Fatalf("%s: unexpected %v", tt.family, a)
			}
		}
	}
}

func TestDialFallsBackToNextAddress(t *testing.T) {
	good, _ := tcpSink(t, "127.0.0.3", 0)
	log := &queryLog{}
	s := startDNS(t, log, 0, "127.0.0.2", "127.0.0.3") // nothing listens on 127.0.0.2:port
	var h hookLog
	f, err := NewConnectorWithHooks(testBroker(fmt.Sprintf("mqtt://broker.test:%d", good.Port()), &scenario.DNS{Resolvers: servers(s), Family: "ipv4"}), h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dialOnce(t, f); err != nil {
		t.Fatal(err)
	}
	if len(h.conns) != 1 || h.conns[0] != good {
		t.Fatalf("OnConnect = %v, want [%v]", h.conns, good)
	}
}

func TestServerNameKeptForResolvedIP(t *testing.T) {
	pki := newTestPKI(t)
	log := &queryLog{}
	s := startDNS(t, log, 0, "127.0.0.1")
	dnsCfg := &scenario.DNS{Resolvers: servers(s), Family: "ipv4"}
	for _, tt := range []struct {
		name, serverName, mode string
		leaf                   func() tls.Certificate
		want                   string
	}{
		{"san", "", "", func() tls.Certificate { return pki.issue(t, cnLeaf("", "broker.test")) }, "broker.test"},
		{"override", "other.test", "", func() tls.Certificate { return pki.issue(t, cnLeaf("", "other.test")) }, "other.test"},
		{"cn-fallback", "", "cn-fallback", func() tls.Certificate { return pki.issue(t, cnLeaf("broker.test")) }, "broker.test"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := serveTLS(t, tt.leaf(), false)
			u, _ := url.Parse(raw)
			b := testBroker("mqtts://broker.test:"+u.Port(), dnsCfg)
			b.TLS = scenario.TLS{CAPEM: string(pki.pem), ServerName: tt.serverName, HostnameVerification: tt.mode}
			f, err := NewConnectorWithHooks(b, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			c, err := dialOnce(t, f)
			if err != nil {
				t.Fatal(err)
			}
			tc, ok := c.(*alertConn).Conn.(*tls.Conn)
			if !ok {
				t.Fatalf("conn is %T", c.(*alertConn).Conn)
			}
			if got := tc.ConnectionState().ServerName; got != tt.want {
				t.Fatalf("ServerName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLookupUsesRootedName(t *testing.T) {
	remote, _ := tcpSink(t, "127.0.0.1", 0)
	log := &queryLog{}
	s := startDNS(t, log, 0, "127.0.0.1")
	f, err := NewConnectorWithHooks(testBroker(fmt.Sprintf("mqtt://broker.test:%d", remote.Port()), &scenario.DNS{Resolvers: servers(s), Family: "any"}), Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dialOnce(t, f); err != nil {
		t.Fatal(err)
	}
	qs := log.all()
	if len(qs) == 0 {
		t.Fatal("no queries")
	}
	for _, q := range qs {
		if q.name != "broker.test." {
			t.Fatalf("question name %q, want broker.test.", q.name)
		}
	}
}

func TestNoDNSBlockUsesSystemDialer(t *testing.T) {
	remote, _ := tcpSink(t, "127.0.0.1", 0)
	var h hookLog
	f, err := NewConnectorWithHooks(testBroker("mqtt://"+remote.String(), nil), h.hooks())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dialOnce(t, f); err != nil {
		t.Fatal(err)
	}
	if len(h.resolved) != 0 || len(h.errs) != 0 || len(h.conns) != 1 || h.conns[0] != remote {
		t.Fatalf("hooks: resolved=%v errs=%v conns=%v", h.resolved, h.errs, h.conns)
	}
}

func TestClientRemoteAddr(t *testing.T) {
	mb := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	_ = mb.AddHook(new(auth.AllowHook), nil)
	l := listeners.NewTCP(listeners.Config{ID: "remote", Address: "127.0.0.1:0"})
	if err := mb.AddListener(l); err != nil {
		t.Fatal(err)
	}
	_ = mb.Serve()
	defer mb.Close()
	want := netip.MustParseAddrPort(l.Address())
	for _, v := range []string{"3.1.1", "5"} {
		cfg := testBroker("mqtt://"+l.Address(), nil)
		cfg.Version = v
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, err := Connect(ctx, cfg, "remote-"+v, func([]byte) {})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if got := c.RemoteAddr(); got != want {
			t.Fatalf("v%s RemoteAddr = %v, want %v", v, got, want)
		}
		c.Close()
	}
}
