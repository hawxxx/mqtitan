//go:build !linux

package platform

type Check struct{ Name, Value, Status, Recommendation string }

func Doctor() []Check {
	return []Check{{"platform", "unsupported", "WARN", "Run doctor on Linux load-generator host"}}
}
func MemoryTotal() string       { return "unknown" }
func CurrentCapacity() Capacity { return Capacity{} }
