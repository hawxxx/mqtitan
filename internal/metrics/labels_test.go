package metrics

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"unicode/utf8"
)

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

func find(l []LabelCount, key string) (LabelCount, bool) {
	for _, r := range l {
		if r.Key == key {
			return r, true
		}
	}
	return LabelCount{}, false
}

func TestRecordRemoteCountsAndActive(t *testing.T) {
	c := New()
	a, b := ap("192.0.2.1:1883"), ap("192.0.2.2:1883")
	for range 3 {
		c.RecordRemote(a)
	}
	c.RecordRemote(b)
	c.ReleaseRemote(a)
	got := c.Snapshot().RemoteAddresses
	want := []LabelCount{{Key: "192.0.2.1:1883", Connects: 3, Active: 2}, {Key: "192.0.2.2:1883", Connects: 1, Active: 1}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if c.Snapshot().AddressLabels != nil {
		t.Fatal("address labels reported without configuration")
	}
}

func TestAddressLabelLongestPrefix(t *testing.T) {
	c := New()
	c.SetAddressLabels([]netip.Prefix{netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("198.51.100.0/28")}, []string{"wide", "narrow"})
	c.RecordRemote(ap("198.51.100.3:1883"))
	c.RecordRemote(ap("192.0.2.1:1883"))
	c.RecordRemote(ap("[::ffff:198.51.100.3]:1883"))
	c.RecordRemote(ap("198.51.100.200:1883"))
	s := c.Snapshot()
	for key, want := range map[string]uint64{"narrow": 2, "unlabelled": 1, "wide": 1} {
		if r, ok := find(s.AddressLabels, key); !ok || r.Connects != want || r.Active != int64(want) {
			t.Fatalf("%s = %+v, labels %+v", key, r, s.AddressLabels)
		}
	}
	if r, ok := find(s.RemoteAddresses, "198.51.100.3:1883"); !ok || r.Connects != 2 || r.Label != "narrow" {
		t.Fatalf("mapped address not unmapped: %+v", s.RemoteAddresses)
	}
	c.ReleaseRemote(ap("[::ffff:198.51.100.3]:1883"))
	if r, _ := find(c.Snapshot().AddressLabels, "narrow"); r.Active != 1 {
		t.Fatalf("release did not unmap: %+v", r)
	}
}

func TestLabelCardinalityCap(t *testing.T) {
	c := New()
	for i := range 1100 {
		c.RecordRemote(netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), 1883))
		c.RecordDNSAnswers([]netip.Addr{netip.AddrFrom4([4]byte{10, 1, byte(i >> 8), byte(i)})})
	}
	s := c.Snapshot()
	for name, l := range map[string][]LabelCount{"remote": s.RemoteAddresses, "dns": s.DNSAnswers} {
		if len(l) != MaxLabelKeys+1 {
			t.Fatalf("%s rows = %d", name, len(l))
		}
		if o, ok := find(l, OtherKey); !ok || o.Connects != 76 {
			t.Fatalf("%s other = %+v", name, o)
		}
	}
}

func TestRecordRemoteNoAlloc(t *testing.T) {
	c := New()
	c.SetAddressLabels([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, []string{"x"})
	a := ap("192.0.2.1:1883")
	c.RecordRemote(a)
	c.ReleaseRemote(a)
	if n := testing.AllocsPerRun(1000, func() { c.RecordRemote(a); c.ReleaseRemote(a) }); n != 0 {
		t.Fatalf("allocs = %v", n)
	}
}

func TestMergeSnapshotsLabels(t *testing.T) {
	a := Snapshot{RemoteAddresses: []LabelCount{{Key: "b", Connects: 2, Active: 1}, {Key: "a", Connects: 1, Active: 1}}, AddressLabels: []LabelCount{{Key: "x", Connects: 1}}}
	b := Snapshot{RemoteAddresses: []LabelCount{{Key: "b", Connects: 3, Active: 2}, {Key: "c", Connects: 1, Active: -5}}}
	got := MergeSnapshots([]Snapshot{a, b}, nil)
	want := []LabelCount{{Key: "a", Connects: 1, Active: 1}, {Key: "b", Connects: 5, Active: 3}, {Key: "c", Connects: 1}}
	if len(got.RemoteAddresses) != 3 || got.RemoteAddresses[0] != want[0] || got.RemoteAddresses[1] != want[1] || got.RemoteAddresses[2] != want[2] {
		t.Fatalf("got %+v", got.RemoteAddresses)
	}
	// Hostile input: far more than the cap, from two workers.
	var big1, big2 Snapshot
	for i := range 3000 {
		big1.DNSErrors = append(big1.DNSErrors, LabelCount{Key: fmt.Sprintf("k%04d", i), Connects: 1})
		big2.DNSErrors = append(big2.DNSErrors, LabelCount{Key: fmt.Sprintf("k%04d", i+1500), Connects: 1})
	}
	m := MergeSnapshots([]Snapshot{big1, big2}, nil).DNSErrors
	if len(m) != MaxLabelKeys+1 {
		t.Fatalf("rows = %d", len(m))
	}
	var total uint64
	for i, r := range m {
		total += r.Connects
		if i > 0 && m[i-1].Key > r.Key {
			t.Fatal("not sorted")
		}
	}
	if total != 6000 {
		t.Fatalf("total = %d, folded counts lost", total)
	}
}

func TestDNSAnswersAndErrorsCounted(t *testing.T) {
	c := New()
	x, y := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")
	c.RecordDNSAnswers([]netip.Addr{x, y})
	c.RecordDNSAnswers([]netip.Addr{x, y})
	c.RecordDNSAnswers([]netip.Addr{netip.MustParseAddr("::ffff:192.0.2.1")})
	r := ap("192.0.2.53:53")
	c.RecordDNSError(r)
	c.RecordDNSError(r)
	s := c.Snapshot()
	if v, _ := find(s.DNSAnswers, "192.0.2.1"); v.Connects != 3 {
		t.Fatalf("x = %+v", s.DNSAnswers)
	}
	if v, _ := find(s.DNSAnswers, "192.0.2.2"); v.Connects != 2 {
		t.Fatalf("y = %+v", s.DNSAnswers)
	}
	if v, _ := find(s.DNSErrors, "192.0.2.53:53"); v.Connects != 2 || len(s.DNSErrors) != 1 {
		t.Fatalf("errors = %+v", s.DNSErrors)
	}
}

func TestMergeLabelCountsTruncatesOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("a", 127) + "€" + "tail" // the rune spans bytes 127-129
	got := MergeSnapshots([]Snapshot{{AddressLabels: []LabelCount{{Key: long, Label: long, Connects: 1}}}}, nil).AddressLabels
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	for _, v := range []string{got[0].Key, got[0].Label} {
		if !utf8.ValidString(v) || v != strings.Repeat("a", 127) {
			t.Fatalf("truncated to %q (%d bytes)", v, len(v))
		}
	}
	ok := strings.Repeat("b", 125) + "€" // exactly 128 bytes stays whole
	if got := MergeSnapshots([]Snapshot{{DNSErrors: []LabelCount{{Key: ok, Connects: 1}}}}, nil).DNSErrors; got[0].Key != ok {
		t.Fatalf("got %q", got[0].Key)
	}
}
