package platform

import (
	"os"
	"runtime"
	"time"
)

// Host describes a load generator for operators: what it runs on and the
// limits that bound how many connections it can hold. Zero values mean
// unknown.
type Host struct {
	Hostname         string    `json:"hostname,omitempty"`
	OS               string    `json:"os"`
	Arch             string    `json:"arch"`
	Distro           string    `json:"distro,omitempty"`
	Kernel           string    `json:"kernel,omitempty"`
	CPUModel         string    `json:"cpuModel,omitempty"`
	CPUs             int       `json:"cpus"`
	CPULimit         float64   `json:"cpuLimit,omitempty"`
	MemoryTotalBytes uint64    `json:"memoryTotalBytes,omitempty"`
	MemoryLimitBytes uint64    `json:"memoryLimitBytes,omitempty"`
	OpenFiles        uint64    `json:"openFiles,omitempty"`
	EphemeralPorts   uint64    `json:"ephemeralPorts,omitempty"`
	Container        string    `json:"container,omitempty"`
	GoVersion        string    `json:"goVersion"`
	Version          string    `json:"version,omitempty"`
	StartedAt        time.Time `json:"startedAt"`
}

func baseHost(version string) Host {
	name, _ := os.Hostname()
	return Host{Hostname: name, OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), GoVersion: runtime.Version(), Version: version, StartedAt: time.Now().UTC()}
}
