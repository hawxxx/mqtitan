package metrics

import (
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// MaxLabelKeys bounds every label list: further keys fold into OtherKey.
const MaxLabelKeys = 1024

const (
	// OtherKey collects the keys beyond MaxLabelKeys.
	OtherKey = "other"
	// UnlabelledKey is the address label of addresses no prefix matches.
	UnlabelledKey = "unlabelled"
	maxKeyBytes   = 128
)

// LabelCount is one bounded counter row.
type LabelCount struct {
	Key      string `json:"key"`
	Label    string `json:"label,omitempty"`
	Connects uint64 `json:"connects"`
	Active   int64  `json:"active"`
}

type labelEntry struct {
	connects atomic.Uint64
	active   atomic.Int64
}

// labelSet counts by comparable key with at most MaxLabelKeys distinct keys.
type labelSet[K comparable] struct {
	m     sync.Map // K -> *labelEntry
	size  atomic.Int32
	other labelEntry
}

// entry returns the counter for k, creating it while under the cap. The
// steady-state path (existing key) does not allocate.
func (s *labelSet[K]) entry(k K) *labelEntry {
	if v, ok := s.m.Load(k); ok {
		return v.(*labelEntry)
	}
	if s.size.Add(1) > MaxLabelKeys {
		s.size.Add(-1)
		return &s.other
	}
	v, loaded := s.m.LoadOrStore(k, new(labelEntry))
	if loaded {
		s.size.Add(-1)
	}
	return v.(*labelEntry)
}

// rows renders the set with key and label formatting; sorted by key.
func (s *labelSet[K]) rows(key func(K) string, label func(K) string) []LabelCount {
	var out []LabelCount
	s.m.Range(func(k, v any) bool {
		e := v.(*labelEntry)
		kk := k.(K)
		r := LabelCount{Key: key(kk), Connects: e.connects.Load(), Active: e.active.Load()}
		if label != nil {
			r.Label = label(kk)
		}
		out = append(out, r)
		return true
	})
	if n, a := s.other.connects.Load(), s.other.active.Load(); n > 0 || a != 0 {
		out = append(out, LabelCount{Key: OtherKey, Connects: n, Active: a})
	}
	sortCounts(out)
	return out
}

func sortCounts(l []LabelCount) {
	sort.SliceStable(l, func(i, j int) bool { return l[i].Key < l[j].Key })
}

// labelConfig is an immutable prefix table with one counter per distinct label.
type labelConfig struct {
	prefixes []netip.Prefix // longest first
	index    []int          // prefix -> entry
	names    []string       // entry -> label; the last is UnlabelledKey
	entries  []labelEntry
}

func (lc *labelConfig) find(a netip.Addr) int {
	for i, p := range lc.prefixes {
		if p.Contains(a) {
			return lc.index[i]
		}
	}
	return len(lc.names) - 1
}

// SetAddressLabels configures the prefix to label table; call before any
// connect. Longest prefix wins. prefixes and labels are parallel slices.
func (c *Counters) SetAddressLabels(prefixes []netip.Prefix, labels []string) {
	n := min(len(prefixes), len(labels))
	if n == 0 {
		c.labelCfg.Store(nil)
		return
	}
	type pl struct {
		p netip.Prefix
		l string
	}
	list := make([]pl, n)
	for i := range n {
		list[i] = pl{prefixes[i].Masked(), labels[i]}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].p.Bits() > list[j].p.Bits() })
	lc := &labelConfig{}
	seen := map[string]int{}
	for _, e := range list {
		idx, ok := seen[e.l]
		if !ok {
			idx = len(lc.names)
			seen[e.l] = idx
			lc.names = append(lc.names, e.l)
		}
		lc.prefixes = append(lc.prefixes, e.p)
		lc.index = append(lc.index, idx)
	}
	lc.names = append(lc.names, UnlabelledKey)
	lc.entries = make([]labelEntry, len(lc.names))
	c.labelCfg.Store(lc)
}

func unmapPort(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// RecordRemote counts a connection to ap (Connects and Active).
func (c *Counters) RecordRemote(ap netip.AddrPort) {
	if !ap.IsValid() {
		return
	}
	ap = unmapPort(ap)
	e := c.remotes.entry(ap)
	e.connects.Add(1)
	e.active.Add(1)
	if lc := c.labelCfg.Load(); lc != nil {
		le := &lc.entries[lc.find(ap.Addr())]
		le.connects.Add(1)
		le.active.Add(1)
	}
}

// ReleaseRemote decrements Active for a connection recorded by RecordRemote.
func (c *Counters) ReleaseRemote(ap netip.AddrPort) {
	if !ap.IsValid() {
		return
	}
	ap = unmapPort(ap)
	c.remotes.entry(ap).active.Add(-1)
	if lc := c.labelCfg.Load(); lc != nil {
		lc.entries[lc.find(ap.Addr())].active.Add(-1)
	}
}

// RecordDNSAnswers counts every address of one successful lookup.
func (c *Counters) RecordDNSAnswers(answers []netip.Addr) {
	for _, a := range answers {
		if a.IsValid() {
			c.dnsAnswers.entry(a.Unmap()).connects.Add(1)
		}
	}
}

// RecordDNSError counts one failed attempt against a resolver.
func (c *Counters) RecordDNSError(resolver netip.AddrPort) {
	if resolver.IsValid() {
		c.dnsErrors.entry(unmapPort(resolver)).connects.Add(1)
	}
}

func (c *Counters) fillLabels(s *Snapshot) {
	lc := c.labelCfg.Load()
	var label func(netip.AddrPort) string
	if lc != nil {
		label = func(ap netip.AddrPort) string { return lc.names[lc.find(ap.Addr())] }
		for i := range lc.entries {
			e := &lc.entries[i]
			n, a := e.connects.Load(), e.active.Load()
			if n > 0 || a != 0 {
				s.AddressLabels = append(s.AddressLabels, LabelCount{Key: lc.names[i], Connects: n, Active: a})
			}
		}
		sortCounts(s.AddressLabels)
	}
	s.RemoteAddresses = c.remotes.rows(netip.AddrPort.String, label)
	s.DNSAnswers = c.dnsAnswers.rows(netip.Addr.String, nil)
	s.DNSErrors = c.dnsErrors.rows(netip.AddrPort.String, nil)
}

// mergeLabelCounts sums src into dst by key. Untrusted input is bounded:
// keys are truncated, negative actives clamped, and when more than
// MaxLabelKeys distinct keys result, the busiest are kept and the rest fold
// into OtherKey. The result is sorted by key.
func mergeLabelCounts(dst, src []LabelCount) []LabelCount {
	if len(src) == 0 {
		return dst
	}
	idx := make(map[string]int, len(dst)+len(src))
	for i, r := range dst {
		idx[r.Key] = i
	}
	for _, r := range src {
		r.Key, r.Label = truncateKey(r.Key), truncateKey(r.Label)
		r.Active = max(0, r.Active)
		if i, ok := idx[r.Key]; ok {
			dst[i].Connects += r.Connects
			dst[i].Active += r.Active
			if dst[i].Label == "" {
				dst[i].Label = r.Label
			}
			continue
		}
		idx[r.Key] = len(dst)
		dst = append(dst, r)
	}
	return capLabelCounts(dst)
}

// truncateKey cuts v to at most maxKeyBytes without splitting a UTF-8 rune.
func truncateKey(v string) string {
	if len(v) <= maxKeyBytes {
		return v
	}
	n := maxKeyBytes
	for n > 0 && !utf8.RuneStart(v[n]) {
		n--
	}
	return v[:n]
}

func capLabelCounts(l []LabelCount) []LabelCount {
	var other LabelCount
	rest := l[:0:0]
	for _, r := range l {
		if r.Key == OtherKey {
			other.Connects += r.Connects
			other.Active += r.Active
		} else {
			rest = append(rest, r)
		}
	}
	if len(rest) > MaxLabelKeys {
		sort.SliceStable(rest, func(i, j int) bool {
			if rest[i].Connects != rest[j].Connects {
				return rest[i].Connects > rest[j].Connects
			}
			return rest[i].Key < rest[j].Key
		})
		for _, r := range rest[MaxLabelKeys:] {
			other.Connects += r.Connects
			other.Active += r.Active
		}
		rest = rest[:MaxLabelKeys]
	}
	if other.Connects > 0 || other.Active != 0 {
		other.Key = OtherKey
		rest = append(rest, other)
	}
	sortCounts(rest)
	return rest
}
