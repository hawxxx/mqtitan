//go:build linux

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Reader interface {
	ReadFile(string) ([]byte, error)
	ReadDir(string) ([]string, error)
}
type hostReader struct{}

func (hostReader) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (hostReader) ReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}
func read(r Reader, path string) string {
	b, err := r.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
func integer(v string) uint64 {
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
func memAvailable(r Reader) uint64 {
	for _, line := range strings.Split(read(r, "/proc/meminfo"), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "MemAvailable:" && f[2] == "kB" {
			n := integer(f[1])
			if n <= ^uint64(0)/1024 {
				return n * 1024
			}
		}
	}
	return 0
}
func portRange(r Reader) uint64 {
	f := strings.Fields(read(r, "/proc/sys/net/ipv4/ip_local_port_range"))
	if len(f) != 2 {
		return 0
	}
	low, high := integer(f[0]), integer(f[1])
	if low == 0 || high < low || high > 65535 {
		return 0
	}
	return high - low + 1
}
func ReadCapacity(r Reader, openFiles uint64) Capacity {
	c := Capacity{OpenFiles: openFiles, AvailableMemoryBytes: memAvailable(r), EphemeralPorts: portRange(r)}
	names, _ := r.ReadDir("/sys/class/net")
	for _, name := range names {
		if name == "lo" || filepath.Base(name) != name {
			continue
		}
		speed := integer(read(r, "/sys/class/net/"+name+"/speed"))
		if speed <= ^uint64(0)/1000000 {
			c.NICBitsPerSecond = max(c.NICBitsPerSecond, speed*1000000)
		}
	}
	return c
}
func Diagnose(r Reader, cpus int, openFiles uint64) []Check {
	c := ReadCapacity(r, openFiles)
	checks := []Check{{"cpu", strconv.Itoa(cpus), "INFO", "CPU count is not a clients-per-core capacity measurement"}, {"open files", fmt.Sprint(openFiles), "INFO", "Review process/container soft limits against the plan"}, {"available memory", fmt.Sprintf("%d bytes", c.AvailableMemoryBytes), "INFO", "Host availability excludes independent container memory limits"}}
	ports := Check{"ephemeral ports", read(r, "/proc/sys/net/ipv4/ip_local_port_range"), "INFO", "Each source/destination tuple has a finite port budget"}
	if c.EphemeralPorts == 0 {
		ports.Value = "unavailable or malformed"
		ports.Status = "WARN"
	}
	checks = append(checks, ports)
	for _, item := range []struct{ name, path string }{{"system file max", "/proc/sys/fs/file-max"}, {"TCP receive buffers", "/proc/sys/net/ipv4/tcp_rmem"}, {"TCP send buffers", "/proc/sys/net/ipv4/tcp_wmem"}, {"TIME_WAIT reuse", "/proc/sys/net/ipv4/tcp_tw_reuse"}} {
		value := read(r, item.path)
		status := "INFO"
		if value == "" {
			value = "unavailable"
		}
		if strings.Contains(item.name, "buffers") {
			f := strings.Fields(value)
			if len(f) != 3 || integer(f[0]) == 0 || integer(f[1]) == 0 || integer(f[2]) == 0 {
				status = "WARN"
				value = "unavailable or malformed"
			}
		}
		checks = append(checks, Check{item.name, value, status, "Review host TCP settings against measured workload; doctor never changes sysctls"})
	}
	tw := "unavailable"
	for _, line := range strings.Split(read(r, "/proc/net/sockstat"), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == "TCP:" {
			for i := 1; i+1 < len(f); i += 2 {
				if f[i] == "tw" {
					if _, err := strconv.ParseUint(f[i+1], 10, 64); err == nil {
						tw = f[i+1]
					}
				}
			}
		}
	}
	checks = append(checks, Check{"TIME_WAIT", tw, "INFO", "Cumulative/current socket state is not a connection-rate benchmark"})
	countText, maxText := read(r, "/proc/sys/net/netfilter/nf_conntrack_count"), read(r, "/proc/sys/net/netfilter/nf_conntrack_max")
	conntrack := Check{"conntrack", "unavailable (module or namespace may not expose it)", "INFO", "Review NAT/conntrack capacity when connections traverse stateful network devices"}
	if countText != "" && integer(maxText) > 0 {
		conntrack.Value = countText + " / " + maxText
		if float64(integer(countText)) >= float64(integer(maxText))*0.8 {
			conntrack.Status = "WARN"
		}
	}
	checks = append(checks, conntrack)
	dns := read(r, "/etc/resolv.conf")
	nameservers := 0
	for _, line := range strings.Split(dns, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == "nameserver" {
			nameservers++
		}
	}
	dnsCheck := Check{"DNS", fmt.Sprintf("%d configured nameservers", nameservers), "INFO", "Review resolver reachability from each worker; names/domains are redacted"}
	if dns == "" {
		dnsCheck.Value = "unavailable"
	}
	checks = append(checks, dnsCheck)
	names, _ := r.ReadDir("/sys/class/net")
	for _, name := range names {
		if name == "lo" || filepath.Base(name) != name {
			continue
		}
		base := "/sys/class/net/" + name
		speed := integer(read(r, base+"/speed"))
		value := "unknown"
		if speed > 0 {
			value = fmt.Sprintf("%d Mbit/s", speed)
		}
		checks = append(checks, Check{"NIC " + name + " speed", value, "INFO", "Reported link speed is not achieved bandwidth; virtual devices may not expose it"})
		rx, tx := read(r, base+"/statistics/rx_bytes"), read(r, base+"/statistics/tx_bytes")
		checks = append(checks, Check{"NIC " + name + " bytes", "rx=" + fallback(rx) + " tx=" + fallback(tx), "INFO", "Counters are cumulative; compare timed samples for traffic rates"})
		dropsRX, dropsTX := read(r, base+"/statistics/rx_dropped"), read(r, base+"/statistics/tx_dropped")
		status := "INFO"
		if integer(dropsRX) > 0 || integer(dropsTX) > 0 {
			status = "WARN"
		}
		checks = append(checks, Check{"NIC " + name + " drops", "rx=" + fallback(dropsRX) + " tx=" + fallback(dropsTX), status, "Cumulative drops may predate this run; compare timed samples"})
	}
	return checks
}
func fallback(v string) string {
	if v == "" {
		return "unavailable"
	}
	return v
}
