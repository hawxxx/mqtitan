package distributed

import (
	"errors"
	"github.com/mqtitan/mqtitan/internal/scenario"
)

// Partition assigns contiguous global indices without changing workload membership.
func Partition(s scenario.Scenario, index, count int) (scenario.Scenario, error) {
	if count <= 0 || index < 0 || index >= count || count > s.Clients.Count {
		return s, errors.New("invalid worker partition")
	}
	base := s.Clients.Count / count
	extra := s.Clients.Count % count
	start := index*base + min(index, extra)
	size := base
	if index < extra {
		size++
	}
	out := s
	out.Clients.Count = size
	out.Clients.SequenceOffset = s.Clients.SequenceOffset + start
	out.Stages = append([]scenario.Stage(nil), s.Stages...)
	for i, st := range out.Stages {
		out.Stages[i].TargetClients = max(0, min(size, st.TargetClients-start))
	}
	out.Workloads = nil
	offset := 0
	for _, w := range s.Workloads {
		from := max(start, offset)
		to := min(start+size, offset+w.Clients)
		offset += w.Clients
		if to > from {
			w.Clients = to - from
			out.Workloads = append(out.Workloads, w)
		}
	}
	if len(out.Workloads) == 0 {
		out.Workloads = []scenario.Workload{{Name: "connections", Type: "connection", Clients: size}}
	}
	return out, nil
}
