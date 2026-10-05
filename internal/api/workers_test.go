package api

import (
	"testing"
	"time"
)

func TestWorkerLeaseExpiryIsVisible(t *testing.T) {
	r := newWorkerRegistry()
	if err := r.register(Worker{ID: "worker-a", CPUCount: 4}); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.workers["worker-a"].LastSeen = time.Now().Add(-time.Minute)
	r.mu.Unlock()
	workers := r.list()
	if len(workers) != 1 || workers[0].Status != "lost" {
		t.Fatalf("dead worker counted healthy: %+v", workers)
	}
}

func TestWorkerIDsAndCapacityBound(t *testing.T) {
	r := newWorkerRegistry()
	for _, id := range []string{"", "../../etc/passwd", "spaces in id"} {
		if err := r.register(Worker{ID: id}); err == nil {
			t.Fatalf("accepted invalid worker id %q", id)
		}
	}
}
