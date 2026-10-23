//go:build linux

package platform

import (
	"os"
	"strconv"
	"strings"
)

func HostInfo(version string) Host {
	h := ReadHost(hostReader{}, baseHost(version), openFileLimit())
	switch {
	case os.Getenv("KUBERNETES_SERVICE_HOST") != "":
		h.Container = "kubernetes"
	case fileExists("/.dockerenv") || fileExists("/run/.containerenv"):
		h.Container = "container"
	}
	return h
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

// ReadHost fills the Linux details of h from procfs, os-release and cgroup v2.
func ReadHost(r Reader, h Host, openFiles uint64) Host {
	h.OpenFiles = openFiles
	h.Kernel = read(r, "/proc/sys/kernel/osrelease")
	for _, line := range strings.Split(read(r, "/etc/os-release"), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			h.Distro = strings.Trim(v, `"'`)
		}
	}
	for _, line := range strings.Split(read(r, "/proc/cpuinfo"), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == "model name" {
			h.CPUModel = strings.Join(strings.Fields(v), " ")
			break
		}
	}
	for _, line := range strings.Split(read(r, "/proc/meminfo"), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "MemTotal:" && f[2] == "kB" {
			h.MemoryTotalBytes = integer(f[1]) * 1024
		}
	}
	if f := strings.Fields(read(r, "/sys/fs/cgroup/cpu.max")); len(f) == 2 && f[0] != "max" {
		quota, _ := strconv.ParseFloat(f[0], 64)
		period, _ := strconv.ParseFloat(f[1], 64)
		if quota > 0 && period > 0 {
			h.CPULimit = quota / period
		}
	}
	if v := read(r, "/sys/fs/cgroup/memory.max"); v != "" && v != "max" {
		h.MemoryLimitBytes = integer(v)
	}
	h.EphemeralPorts = portRange(r)
	return h
}
