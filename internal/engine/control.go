package engine

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
)

const MaxLiveRate = 1_000_000

type Load struct {
	Clients       int     `json:"clients"`
	RatePerClient float64 `json:"ratePerClient"`
	Revision      uint64  `json:"revision"`
}

type controlState struct {
	load    Load
	changed chan struct{}
}

// Control broadcasts only actual updates. Publishers do not poll or allocate per message.
// Replacing one immutable value coalesces updates without an unbounded command queue.
type Control struct {
	capacity int
	mu       sync.Mutex
	state    atomic.Pointer[controlState]
}

func NewControl(capacity int) *Control {
	c := &Control{capacity: capacity}
	c.state.Store(&controlState{changed: make(chan struct{})})
	return c
}

func ValidateLoad(load Load, capacity int) error {
	if load.Clients < 0 || load.Clients > capacity {
		return errors.New("clients must be between zero and the scenario client capacity")
	}
	if math.IsNaN(load.RatePerClient) || math.IsInf(load.RatePerClient, 0) || load.RatePerClient < 0 || load.RatePerClient > MaxLiveRate {
		return errors.New("ratePerClient must be between zero and 1000000 messages/sec")
	}
	if load.Revision == 0 {
		return errors.New("live load revision must be positive")
	}
	return nil
}

func (c *Control) Current() (Load, <-chan struct{}) {
	if c == nil {
		return Load{}, nil
	}
	s := c.state.Load()
	return s.load, s.changed
}

func (c *Control) Apply(load Load) error {
	if err := ValidateLoad(load, c.capacity); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.state.Load()
	if load.Revision <= previous.load.Revision {
		return nil
	}
	c.state.Store(&controlState{load: load, changed: make(chan struct{})})
	close(previous.changed)
	return nil
}
