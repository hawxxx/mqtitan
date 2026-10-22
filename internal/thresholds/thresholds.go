package thresholds

import (
	"fmt"
	"github.com/mqtitan/mqtitan/internal/emqx"
	"github.com/mqtitan/mqtitan/internal/metrics"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Result struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	Observed   string `json:"observed"`
	Passed     bool   `json:"passed"`
}
type expression struct {
	operator string
	limit    float64
	latency  bool
}

func isBalance(name string) bool {
	return name == "broker_connection_cv" || name == "zone_connection_skew"
}

var pattern = regexp.MustCompile(`^(<=|>=|<|>)([0-9]+(?:\.[0-9]+)?)(%|ns|us|µs|ms|s|m)$`)

func parse(name, text string) (expression, error) {
	latency := false
	switch name {
	case "broker_connection_cv", "zone_connection_skew":
	case "connection_error_rate", "message_error_rate", "publish_error_rate", "subscribe_error_rate":
	case "publish_latency_p50", "publish_latency_p95", "publish_latency_p99",
		"connect_latency_p50", "connect_latency_p95", "connect_latency_p99",
		"end_to_end_latency_p50", "end_to_end_latency_p95", "end_to_end_latency_p99":
		latency = true
	default:
		return expression{}, fmt.Errorf("unknown threshold metric %q", name)
	}
	m := pattern.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return expression{}, fmt.Errorf("invalid threshold %q", text)
	}
	value, err := strconv.ParseFloat(m[2], 64)
	if err != nil || math.IsInf(value, 0) {
		return expression{}, fmt.Errorf("invalid threshold limit")
	}
	if latency {
		if m[3] == "%" {
			return expression{}, fmt.Errorf("latency threshold requires duration")
		}
		d, err := time.ParseDuration(m[2] + m[3])
		if err != nil {
			return expression{}, err
		}
		value = float64(d)
	} else {
		if m[3] != "%" || (value > 100 && !isBalance(name)) {
			if isBalance(name) {
				return expression{}, fmt.Errorf("balance threshold requires a percentage")
			}
			return expression{}, fmt.Errorf("error rate requires percentage from 0 to 100")
		}
		value /= 100
	}
	return expression{m[1], value, latency}, nil
}

func Validate(input map[string]string) error {
	for n, e := range input {
		if _, err := parse(n, e); err != nil {
			return err
		}
	}
	return nil
}
func Evaluate(input map[string]string, s metrics.Snapshot, b *emqx.Balance) ([]Result, error) {
	names := make([]string, 0, len(input))
	for n := range input {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Result, 0, len(names))
	for _, n := range names {
		ex, err := parse(n, input[n])
		if err != nil {
			return nil, err
		}
		var value float64
		valid := true
		switch n {
		case "connection_error_rate":
			attempts := s.ConnectAttempts - min(s.ConnectAttempts, s.ConnectCancelled)
			valid = attempts > 0
			if valid {
				value = float64(s.ConnectErrors) / float64(attempts)
			}
		case "message_error_rate", "publish_error_rate":
			attempts := s.PublishAttempts - min(s.PublishAttempts, s.PublishCancelled)
			valid = attempts > 0
			if valid {
				value = float64(s.PublishErrors) / float64(attempts)
			}
		case "subscribe_error_rate":
			attempts := s.Subscriptions + s.SubscribeErrors
			valid = attempts > 0
			if valid {
				value = float64(s.SubscribeErrors) / float64(attempts)
			}
		case "broker_connection_cv":
			valid = b != nil && (len(b.Targets) >= 2 || !b.Complete)
			if valid {
				value = b.TargetCV
			}
		case "zone_connection_skew":
			valid = b != nil && (len(b.Zones) >= 2 || !b.Complete)
			if valid {
				value = b.ZoneSkew
			}
		case "connect_latency_p50":
			value, valid = float64(s.ConnectP50), s.PeakConnected > 0
		case "connect_latency_p95":
			value, valid = float64(s.ConnectP95), s.PeakConnected > 0
		case "connect_latency_p99":
			value, valid = float64(s.ConnectP99), s.PeakConnected > 0
		case "end_to_end_latency_p50":
			value, valid = float64(s.EndToEndP50), s.CorrelationSamples > 0
		case "end_to_end_latency_p95":
			value, valid = float64(s.EndToEndP95), s.CorrelationSamples > 0
		case "end_to_end_latency_p99":
			value, valid = float64(s.EndToEndP99), s.CorrelationSamples > 0
		case "publish_latency_p50":
			value, valid = float64(s.P50), hasLatencySamples(s)
		case "publish_latency_p95":
			value, valid = float64(s.P95), hasLatencySamples(s)
		case "publish_latency_p99":
			value, valid = float64(s.P99), hasLatencySamples(s)
		}
		incomplete := isBalance(n) && valid && !b.Complete
		passed := false
		if valid && !incomplete {
			switch ex.operator {
			case "<":
				passed = value < ex.limit
			case "<=":
				passed = value <= ex.limit
			case ">":
				passed = value > ex.limit
			case ">=":
				passed = value >= ex.limit
			}
		}
		observed := "no observations"
		if incomplete {
			observed = "incomplete broker telemetry"
		} else if valid {
			if ex.latency {
				observed = time.Duration(value).String()
			} else {
				observed = fmt.Sprintf("%.6f%%", value*100)
			}
		}
		out = append(out, Result{n, input[n], observed, passed})
	}
	return out, nil
}

func Passed(results []Result) bool {
	for _, r := range results {
		if !r.Passed {
			return false
		}
	}
	return true
}

// hasLatencySamples requires recorded latency, not just published messages:
// a merged result whose worker histograms were missing or invalid has
// Published > 0 but zero percentiles, which would pass any "<" threshold.
func hasLatencySamples(s metrics.Snapshot) bool {
	var n uint64
	for _, b := range s.Histogram {
		n += b.Count
	}
	return s.PublishLatencyCount > 0 && n > 0
}
