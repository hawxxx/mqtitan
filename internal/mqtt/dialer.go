package mqtt

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mqtitan/mqtitan/internal/scenario"
)

// Hooks observe dialing. Any field may be nil. They run on the dialing
// goroutine and must not block.
type Hooks struct {
	OnResolve      func(answers []netip.Addr)    // once per successful lookup, every answer
	OnResolveError func(resolver netip.AddrPort) // per failed resolver attempt
	OnConnect      func(remote netip.AddrPort)   // after TCP connect succeeds
	// Seed seeds the address shuffle of broker.dns.pick random.
	Seed uint64
	// StrictSecrets is the scenario's safety.strictSecrets: refuse key files
	// with permission or placement findings.
	StrictSecrets bool
}

// resolveAttemptTimeout bounds a single resolver attempt; tests lower it.
var resolveAttemptTimeout = 2 * time.Second

type resolverEntry struct {
	r    *net.Resolver
	addr netip.AddrPort // zero for the system resolver
}

// dialer resolves the broker host with the configured resolvers and connects
// to the answers. A nil dns block dials host:port directly.
type dialer struct {
	dns        *scenario.DNS
	hooks      Hooks
	host       string
	rootedHost string
	hostport   string // host:port, for the system dialer
	port       uint16
	resolvers  []resolverEntry
	next       atomic.Uint32
	network    string // LookupNetIP network
	random     bool

	rngMu sync.Mutex
	rng   *rand.Rand

	cacheMu sync.Mutex
	cache   []netip.Addr
}

func newDialer(host, port string, d *scenario.DNS, h Hooks) *dialer {
	p, _ := strconv.ParseUint(port, 10, 16)
	dl := &dialer{dns: d, hooks: h, host: host, rootedHost: rooted(host), hostport: net.JoinHostPort(host, port), port: uint16(p), network: "ip"}
	if d == nil {
		return dl
	}
	switch d.Family {
	case "ipv4":
		dl.network = "ip4"
	case "ipv6":
		dl.network = "ip6"
	}
	dl.random = d.Pick == "random"
	dl.rng = rand.New(rand.NewPCG(h.Seed, 0))
	for _, s := range d.Resolvers {
		ap, err := netip.ParseAddrPort(s)
		if err != nil { // rejected by scenario validation
			continue
		}
		fixed := ap.String()
		dl.resolvers = append(dl.resolvers, resolverEntry{&net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fixed)
		}}, ap})
	}
	if len(dl.resolvers) == 0 {
		dl.resolvers = []resolverEntry{{r: &net.Resolver{PreferGo: true}}}
	}
	return dl
}

// rooted appends a trailing dot so resolv.conf search/ndots never alter the
// question sent to the configured resolvers.
func rooted(host string) string {
	if host == "" || host[len(host)-1] == '.' {
		return host
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return host
	}
	return host + "."
}

func stageDNS(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &StageError{StageDNS, err}
}

// lookup returns the filtered, unmapped answers for the broker host.
func (d *dialer) lookup(ctx context.Context) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(d.host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	name := d.rootedHost
	n := uint32(len(d.resolvers))
	start := d.next.Add(1) - 1
	var last error
	for i := range n {
		e := d.resolvers[(start+i)%n]
		actx, cancel := context.WithTimeout(ctx, resolveAttemptTimeout)
		raw, err := e.r.LookupNetIP(actx, d.network, name)
		cancel()
		if err == nil {
			out := raw[:0:0]
			for _, a := range raw {
				a = a.Unmap()
				if (d.network == "ip4" && !a.Is4()) || (d.network == "ip6" && !a.Is6()) {
					continue
				}
				out = append(out, a)
			}
			if len(out) == 0 {
				last = &net.DNSError{Err: "no addresses of the requested family", Name: d.host, IsNotFound: true}
				break
			}
			if d.hooks.OnResolve != nil {
				d.hooks.OnResolve(out)
			}
			return out, nil
		}
		if ctx.Err() != nil { // the caller's deadline or cancellation, not the resolver's fault
			if ctx.Err() == context.Canceled {
				return nil, err
			}
			last = err
			break
		}
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			last = err
			break
		}
		last = err
		if d.hooks.OnResolveError != nil {
			d.hooks.OnResolveError(e.addr)
		}
	}
	return nil, stageDNS(last)
}

func (d *dialer) addresses(ctx context.Context) ([]netip.Addr, error) {
	var addrs []netip.Addr
	if d.dns.ResolveEachDial {
		var err error
		if addrs, err = d.lookup(ctx); err != nil {
			return nil, err
		}
	} else {
		d.cacheMu.Lock()
		defer d.cacheMu.Unlock()
		if d.cache == nil {
			if err := ctx.Err(); err != nil {
				return nil, stageDNS(err)
			}
			a, err := d.lookup(ctx)
			if err != nil {
				return nil, err
			}
			d.cache = a
		}
		addrs = d.cache
	}
	if d.random && len(addrs) > 1 {
		addrs = append([]netip.Addr(nil), addrs...)
		d.rngMu.Lock()
		d.rng.Shuffle(len(addrs), func(i, j int) { addrs[i], addrs[j] = addrs[j], addrs[i] })
		d.rngMu.Unlock()
	}
	return addrs, nil
}

func remoteOf(c net.Conn) netip.AddrPort {
	if t, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		ap := t.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	}
	return netip.AddrPort{}
}

// dialTCP connects to hostport (host:port as in the broker URL).
func (d *dialer) dialTCP(ctx context.Context) (net.Conn, netip.AddrPort, error) {
	nd := &net.Dialer{}
	if d.dns == nil {
		conn, err := nd.DialContext(ctx, "tcp", d.hostport)
		if err != nil {
			stage := StageTCP
			var dns *net.DNSError
			if errors.As(err, &dns) {
				stage = StageDNS
			}
			return nil, netip.AddrPort{}, &StageError{stage, err}
		}
		return d.connected(conn)
	}
	addrs, err := d.addresses(ctx)
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	var last error
	for _, a := range addrs {
		conn, err := nd.DialTCP(ctx, "tcp", netip.AddrPort{}, netip.AddrPortFrom(a, d.port))
		if err == nil {
			return d.connected(conn)
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, netip.AddrPort{}, &StageError{StageTCP, last}
}

func (d *dialer) connected(c net.Conn) (net.Conn, netip.AddrPort, error) {
	remote := remoteOf(c)
	if d.hooks.OnConnect != nil {
		d.hooks.OnConnect(remote)
	}
	return c, remote, nil
}

// ResolveBroker performs one lookup of the broker host with the scenario's
// dns settings and returns the filtered answers.
func ResolveBroker(ctx context.Context, b scenario.Broker) ([]netip.Addr, error) {
	u, err := url.Parse(b.URL)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("invalid broker URL")
	}
	dns := b.DNS
	if dns == nil {
		dns = &scenario.DNS{}
	}
	return newDialer(u.Hostname(), portOf(u), dns, Hooks{}).lookup(ctx)
}
