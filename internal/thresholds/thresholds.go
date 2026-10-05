package thresholds

import (
	"fmt"
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

var pattern = regexp.MustCompile(`^(<=|>=|<|>)([0-9]+(?:\.[0-9]+)?)(%|ns|us|µs|ms|s|m)$`)

func parse(name, text string) (expression, error) {
	latency := false
	switch name {
	case "connection_error_rate", "message_error_rate", "publish_error_rate":
	case "publish_latency_p50", "publish_latency_p95", "publish_latency_p99":
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
		if m[3] != "%" || value > 100 {
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
func Evaluate(input map[string]string, s metrics.Snapshot) ([]Result, error) {
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
		case "publish_latency_p50":
			value = float64(s.P50)
			valid = s.Published > 0
		case "publish_latency_p95":
			value = float64(s.P95)
			valid = s.Published > 0
		case "publish_latency_p99":
			value = float64(s.P99)
			valid = s.Published > 0
		}
		passed := false
		if valid {
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
		if valid {
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
