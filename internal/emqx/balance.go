package emqx

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

// Group is a connection total for one target or zone.
type Group struct {
	Key         string  `json:"key"`
	Connections float64 `json:"connections"`
}

// Balance describes how evenly connections are spread across broker targets
// and zones in one fleet sample.
type Balance struct {
	Timestamp   time.Time `json:"timestamp"`
	Complete    bool      `json:"complete"`
	Targets     []Group   `json:"targets"`
	Zones       []Group   `json:"zones"`       // empty unless every target has a zone
	TargetCV    float64   `json:"targetCV"`    // population stddev / mean
	TargetRatio float64   `json:"targetRatio"` // max/min; +Inf when min == 0 (JSON null)
	ZoneSkew    float64   `json:"zoneSkew"`    // (max-min)/mean of zone totals
	ZoneRatio   float64   `json:"zoneRatio"`
}

type balanceJSON Balance

type balanceWire struct {
	balanceJSON
	TargetRatio *float64 `json:"targetRatio"`
	ZoneRatio   *float64 `json:"zoneRatio"`
}

func finite(v float64) *float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return nil
	}
	return &v
}

func infIfNil(p *float64) float64 {
	if p == nil {
		return math.Inf(1)
	}
	return *p
}

// MarshalJSON encodes an infinite ratio as null, which encoding/json would
// otherwise refuse.
func (b Balance) MarshalJSON() ([]byte, error) {
	return json.Marshal(balanceWire{balanceJSON(b), finite(b.TargetRatio), finite(b.ZoneRatio)})
}

// UnmarshalJSON restores a null ratio as +Inf.
func (b *Balance) UnmarshalJSON(data []byte) error {
	var w balanceWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*b = Balance(w.balanceJSON)
	b.TargetRatio, b.ZoneRatio = infIfNil(w.TargetRatio), infIfNil(w.ZoneRatio)
	return nil
}

func total(g []Group) (sum float64) {
	for _, x := range g {
		sum += x.Connections
	}
	return
}

// CVObserved reports whether TargetCV is meaningful: at least two targets
// holding connections. An all-zero fleet has no observable balance.
func (b Balance) CVObserved() bool { return len(b.Targets) >= 2 && total(b.Targets) > 0 }

// SkewObserved reports whether ZoneSkew is meaningful.
func (b Balance) SkewObserved() bool { return len(b.Zones) >= 2 && total(b.Zones) > 0 }

func groupsOf(m map[string]float64) []Group {
	out := make([]Group, 0, len(m))
	for k, v := range m {
		out = append(out, Group{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// spread returns mean, population standard deviation, min and max.
func spread(g []Group) (mean, sd, lo, hi float64) {
	if len(g) == 0 {
		return
	}
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, x := range g {
		mean += x.Connections
		lo, hi = math.Min(lo, x.Connections), math.Max(hi, x.Connections)
	}
	mean /= float64(len(g))
	for _, x := range g {
		sd += (x.Connections - mean) * (x.Connections - mean)
	}
	return mean, math.Sqrt(sd / float64(len(g))), lo, hi
}

func ratio(lo, hi float64) float64 {
	switch {
	case hi == 0:
		return 1
	case lo == 0:
		return math.Inf(1)
	}
	return hi / lo
}

// ComputeBalance summarizes one sample. Targets with an error are excluded,
// and an incomplete sample yields an incomplete balance.
func ComputeBalance(s FleetSample) Balance {
	targets, zones := map[string]float64{}, map[string]float64{}
	allZones := true
	for _, t := range s.Targets {
		if t.Error != "" {
			continue
		}
		targets[t.Target] += t.Connections
		if t.Zone == "" {
			allZones = false
		} else {
			zones[t.Zone] += t.Connections
		}
	}
	b := Balance{Timestamp: s.Timestamp, Complete: s.Complete, Targets: groupsOf(targets), Zones: []Group{}}
	if allZones && len(zones) > 0 {
		b.Zones = groupsOf(zones)
	}
	mean, sd, lo, hi := spread(b.Targets)
	if mean > 0 {
		b.TargetCV = sd / mean
	}
	b.TargetRatio = ratio(lo, hi)
	zmean, _, zlo, zhi := spread(b.Zones)
	if zmean > 0 {
		b.ZoneSkew = (zhi - zlo) / zmean
	}
	b.ZoneRatio = ratio(zlo, zhi)
	return b
}

// PeakBalance returns the balance of the non-baseline sample with the highest
// total connections. Complete is false when any later (or simultaneous)
// non-baseline sample was incomplete.
func PeakBalance(samples []FleetSample) (Balance, bool) {
	best, found := -1, false
	var bestTotal float64
	for i, s := range samples {
		if s.Baseline {
			continue
		}
		total := 0.0
		for _, t := range s.Targets {
			if t.Error == "" {
				total += t.Connections
			}
		}
		if !found || total > bestTotal {
			best, bestTotal, found = i, total, true
		}
	}
	if !found {
		return Balance{}, false
	}
	b := ComputeBalance(samples[best])
	// A telemetry failure at or after the peak means the peak cannot be trusted.
	for _, s := range samples {
		if !s.Baseline && !s.Complete && !s.Timestamp.Before(samples[best].Timestamp) {
			b.Complete = false
		}
	}
	return b, true
}
