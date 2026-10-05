//go:build linux

package platform

import (
	"runtime"
	"strings"
	"syscall"
)

type Check struct{ Name, Value, Status, Recommendation string }

func Doctor() []Check { return Diagnose(hostReader{}, runtime.NumCPU(), openFileLimit()) }
func openFileLimit() uint64 {
	var limit syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit) != nil {
		return 0
	}
	return limit.Cur
}
func CurrentCapacity() Capacity { return ReadCapacity(hostReader{}, openFileLimit()) }
func MemoryTotal() string {
	for _, line := range strings.Split(read(hostReader{}, "/proc/meminfo"), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "MemTotal:"))
		}
	}
	return "unknown"
}
