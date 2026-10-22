//go:build linux

package platform

import "testing"

func TestReadHostParsesProcAndCgroup(t *testing.T) {
	f := fixtureReader{files: map[string]string{
		"/proc/sys/kernel/osrelease":             "6.6.0-test\n",
		"/etc/os-release":                        "NAME=Alpine\nPRETTY_NAME=\"Alpine Linux v3.20\"\n",
		"/proc/cpuinfo":                          "processor\t: 0\nmodel name\t: AMD   EPYC 7R13\n",
		"/proc/meminfo":                          "MemTotal:       2048 kB\n",
		"/sys/fs/cgroup/cpu.max":                 "200000 100000\n",
		"/sys/fs/cgroup/memory.max":              "1073741824\n",
		"/proc/sys/net/ipv4/ip_local_port_range": "1024\t65000\n",
	}}
	h := ReadHost(f, Host{OS: "linux"}, 65536)
	if h.Kernel != "6.6.0-test" || h.Distro != "Alpine Linux v3.20" || h.CPUModel != "AMD EPYC 7R13" {
		t.Fatalf("identity: %+v", h)
	}
	if h.MemoryTotalBytes != 2048*1024 || h.CPULimit != 2 || h.MemoryLimitBytes != 1<<30 || h.EphemeralPorts != 63977 || h.OpenFiles != 65536 {
		t.Fatalf("limits: %+v", h)
	}
	unlimited := ReadHost(fixtureReader{files: map[string]string{"/sys/fs/cgroup/cpu.max": "max 100000", "/sys/fs/cgroup/memory.max": "max"}}, Host{}, 0)
	if unlimited.CPULimit != 0 || unlimited.MemoryLimitBytes != 0 {
		t.Fatalf("unlimited cgroup: %+v", unlimited)
	}
}
