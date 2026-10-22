package emqx

import (
	"encoding/json"
	"math"
	"testing"
)

func fleet(zone bool, counts ...float64) FleetSample {
	s := FleetSample{Complete: true}
	for i, c := range counts {
		t := TargetSample{Target: string(rune('a' + i)), Connections: c}
		if zone {
			t.Zone = "z"
		}
		s.Targets = append(s.Targets, t)
	}
	return s
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestComputeBalance(t *testing.T) {
	b := ComputeBalance(fleet(false, 100, 100, 100, 100))
	if !near(b.TargetCV, 0) || !near(b.TargetRatio, 1) || !b.Complete {
		t.Fatalf("%+v", b)
	}
	b = ComputeBalance(fleet(false, 50, 150))
	if !near(b.TargetCV, 0.5) || !near(b.TargetRatio, 3) {
		t.Fatalf("%+v", b)
	}
	zoned := func(a, b2 []float64) FleetSample {
		s := FleetSample{Complete: true}
		for i, c := range a {
			s.Targets = append(s.Targets, TargetSample{Target: "a" + string(rune('0'+i)), Zone: "a", Connections: c})
		}
		for i, c := range b2 {
			s.Targets = append(s.Targets, TargetSample{Target: "b" + string(rune('0'+i)), Zone: "b", Connections: c})
		}
		return s
	}
	b = ComputeBalance(zoned([]float64{100, 100}, []float64{200}))
	if len(b.Zones) != 2 || !near(b.ZoneSkew, 0) || !near(b.ZoneRatio, 1) {
		t.Fatalf("%+v", b)
	}
	b = ComputeBalance(zoned([]float64{300}, []float64{100}))
	if !near(b.ZoneSkew, 1.0) || !near(b.ZoneRatio, 3) {
		t.Fatalf("%+v", b)
	}
}

func TestRatioZeroMin(t *testing.T) {
	b := ComputeBalance(fleet(false, 0, 10))
	if !math.IsInf(b.TargetRatio, 1) {
		t.Fatalf("%+v", b)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["targetRatio"]; !ok || v != nil {
		t.Fatalf("targetRatio = %v in %s", v, raw)
	}
	var back Balance
	if err = json.Unmarshal(raw, &back); err != nil || !math.IsInf(back.TargetRatio, 1) {
		t.Fatalf("%v %+v", err, back)
	}
}

func TestPeakBalance(t *testing.T) {
	base := fleet(false, 9000, 9000)
	base.Baseline = true
	samples := []FleetSample{base, fleet(false, 10, 10), fleet(false, 300, 100), fleet(false, 5, 5)}
	b, ok := PeakBalance(samples)
	if !ok || b.Targets[0].Connections+b.Targets[1].Connections != 400 {
		t.Fatalf("%v %+v", ok, b)
	}
	if _, ok = PeakBalance([]FleetSample{base}); ok {
		t.Fatal("baseline only must be empty")
	}
}

func TestZoneStatsRequireEveryZone(t *testing.T) {
	s := fleet(true, 10, 20)
	s.Targets[1].Zone = ""
	if b := ComputeBalance(s); len(b.Zones) != 0 {
		t.Fatalf("%+v", b.Zones)
	}
}

func TestComputeBalanceExcludesErrorTargets(t *testing.T) {
	s := fleet(false, 100, 100)
	s.Targets = append(s.Targets, TargetSample{Error: "missing label"})
	s.Complete = false
	b := ComputeBalance(s)
	if b.Complete || len(b.Targets) != 2 || !near(b.TargetCV, 0) {
		t.Fatalf("%+v", b)
	}
}
