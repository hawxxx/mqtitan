//go:build linux

package platform

import (
	"errors"
	"strings"
	"testing"
)

type fixtureReader struct {
	files map[string]string
	dirs  map[string][]string
}

func (f fixtureReader) ReadFile(path string) ([]byte, error) {
	v, ok := f.files[path]
	if !ok {
		return nil, errors.New("unavailable")
	}
	return []byte(v), nil
}
func (f fixtureReader) ReadDir(path string) ([]string, error) {
	v, ok := f.dirs[path]
	if !ok {
		return nil, errors.New("unavailable")
	}
	return v, nil
}

func TestDiagnoseReadsNICSocketMemoryAndRedactsDNS(t *testing.T) {
	f := fixtureReader{files: map[string]string{
		"/proc/meminfo":                              "MemTotal: 2000 kB\nMemAvailable: 1000 kB\n",
		"/proc/sys/net/ipv4/ip_local_port_range":     "32768 60999",
		"/proc/sys/net/ipv4/tcp_rmem":                "4096 131072 6291456",
		"/proc/sys/net/ipv4/tcp_wmem":                "4096 16384 4194304",
		"/proc/sys/net/ipv4/tcp_tw_reuse":            "2",
		"/proc/net/sockstat":                         "TCP: inuse 8 orphan 0 tw 17 alloc 10 mem 2\n",
		"/proc/sys/net/netfilter/nf_conntrack_count": "90",
		"/proc/sys/net/netfilter/nf_conntrack_max":   "100",
		"/etc/resolv.conf":                           "nameserver 10.0.0.53\nsearch internal-secret.example\n",
		"/sys/class/net/eth0/speed":                  "1000",
		"/sys/class/net/eth0/statistics/rx_bytes":    "120",
		"/sys/class/net/eth0/statistics/tx_bytes":    "230",
		"/sys/class/net/eth0/statistics/rx_dropped":  "3",
		"/sys/class/net/eth0/statistics/tx_dropped":  "0",
	}, dirs: map[string][]string{"/sys/class/net": {"lo", "eth0"}}}
	checks := Diagnose(f, 8, 65536)
	lookup := map[string]Check{}
	for _, c := range checks {
		lookup[c.Name] = c
		if strings.Contains(c.Value, "internal-secret") || strings.Contains(c.Value, "10.0.0.53") {
			t.Fatal("DNS configuration exposed")
		}
	}
	if lookup["TIME_WAIT"].Value != "17" || lookup["conntrack"].Status != "WARN" || lookup["NIC eth0 speed"].Value != "1000 Mbit/s" || lookup["NIC eth0 drops"].Status != "WARN" {
		t.Fatalf("incorrect diagnostics: %+v", lookup)
	}
	host := ReadCapacity(f, 65536)
	if host.AvailableMemoryBytes != 1024000 || host.EphemeralPorts != 28232 || host.NICBitsPerSecond != 1e9 {
		t.Fatalf("wrong measured values: %+v", host)
	}
}

func TestDiagnoseMalformedInputsCannotPass(t *testing.T) {
	f := fixtureReader{files: map[string]string{"/proc/sys/net/ipv4/ip_local_port_range": "invalid", "/proc/sys/net/ipv4/tcp_rmem": "broken"}, dirs: map[string][]string{}}
	checks := Diagnose(f, 1, 0)
	if len(checks) < 4 {
		t.Fatal("missing unavailable observations")
	}
	for _, c := range checks {
		if (c.Name == "ephemeral ports" || c.Name == "TCP receive buffers") && c.Status == "PASS" {
			t.Fatalf("malformed measurement passed: %+v", c)
		}
	}
	if c := ReadCapacity(f, 0); c.AvailableMemoryBytes != 0 || c.EphemeralPorts != 0 || c.NICBitsPerSecond != 0 {
		t.Fatalf("missing values fabricated: %+v", c)
	}
}
