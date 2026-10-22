package scenario

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Safety holds guardrails that keep a load test away from production data.
type Safety struct {
	ForbiddenTopicPrefixes []string `yaml:"forbiddenTopicPrefixes,omitempty"`
	ClientIDDenyRegex      string   `yaml:"clientIdDenyRegex,omitempty"`
	StrictSecrets          bool     `yaml:"strictSecrets,omitempty"`
}

// renderCount counts client IDs rendered by CheckRendered; tests assert that
// scenarios without safety rules never render.
var renderCount atomic.Int64

// maxSourcePorts is the usable ephemeral port range of one source address
// towards one destination address.
const maxSourcePorts = 25000

func normalisePrefix(p string) string {
	p = strings.TrimSuffix(p, "#")
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

func (sf Safety) prefixes() []string {
	out := make([]string, 0, len(sf.ForbiddenTopicPrefixes))
	for _, p := range sf.ForbiddenTopicPrefixes {
		if n := normalisePrefix(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// check reports static problems: invalid rules and workload topics that
// overlap a forbidden prefix as written (templates are not rendered here).
func (sf Safety) check(s Scenario) []string {
	var problems []string
	for i, p := range sf.ForbiddenTopicPrefixes {
		if normalisePrefix(p) == "" {
			problems = append(problems, fmt.Sprintf("safety.forbiddenTopicPrefixes[%d] must not be empty", i))
		}
	}
	if sf.ClientIDDenyRegex != "" {
		if _, err := regexp.Compile(sf.ClientIDDenyRegex); err != nil {
			problems = append(problems, "safety.clientIdDenyRegex is not a valid regular expression")
		}
	}
	for _, w := range s.Workloads {
		if p, ok := overlapsAny(w.Topic, sf.prefixes()); ok && w.Topic != "" {
			problems = append(problems, fmt.Sprintf("workload %q topic %q matches forbidden prefix %q", w.Name, w.Topic, p))
		}
	}
	return problems
}

func overlapsAny(topic string, prefixes []string) (string, bool) {
	for _, p := range prefixes {
		if filterOverlapsPrefix(topic, p) {
			return p, true
		}
	}
	return "", false
}

// filterOverlapsPrefix reports whether a topic or filter can match any topic
// under prefix (which ends in "/").
func filterOverlapsPrefix(filter, prefix string) bool {
	if strings.HasPrefix(filter, "$share/") {
		rest := strings.SplitN(filter, "/", 3)
		if len(rest) < 3 {
			return false
		}
		filter = rest[2]
	}
	pRest, fRest := strings.TrimSuffix(prefix, "/"), filter
	for {
		var pl, fl string
		var pMore, fMore bool
		pl, pRest, pMore = strings.Cut(pRest, "/")
		fl, fRest, fMore = strings.Cut(fRest, "/")
		switch fl {
		case "#":
			return true
		case "+", pl:
		default:
			return false
		}
		if !pMore {
			return fMore // prefix ends with "/": the topic needs another level
		}
		if !fMore {
			return false // filter exhausted before the prefix
		}
	}
}

// needsRenderCheck is true when rendered IDs or topics can matter.
func (s Scenario) needsRenderCheck() bool {
	if s.Safety.ClientIDDenyRegex != "" {
		return true
	}
	if len(s.Safety.prefixes()) > 0 {
		for _, w := range s.Workloads {
			if strings.Contains(w.Topic, "${") {
				return true
			}
		}
	}
	return false
}

// CheckRendered renders the client IDs for sequences from+1 through to (the
// engine's global sequence range) and their templated topics, the way the
// engine does, and returns the first one a safety rule rejects.
func (s Scenario) CheckRendered(worker string, from, to int) error {
	var deny *regexp.Regexp
	if s.Safety.ClientIDDenyRegex != "" {
		var err error
		if deny, err = regexp.Compile(s.Safety.ClientIDDenyRegex); err != nil {
			return fmt.Errorf("safety.clientIdDenyRegex is not a valid regular expression")
		}
	}
	prefixes := s.Safety.prefixes()
	idTemplate := compileRenderer(s.Clients.IDTemplate, worker)
	topics := make([]*renderer, len(s.Workloads))
	for i, w := range s.Workloads {
		if strings.Contains(w.Topic, "${") {
			topics[i] = compileRenderer(w.Topic, worker)
		}
	}
	end := 0
	wi := 0
	var idBuf, topicBuf []byte
	for local := 1; local <= to-from; local++ {
		for wi < len(s.Workloads) && local > end+s.Workloads[wi].Clients {
			end += s.Workloads[wi].Clients
			wi++
		}
		seq := local + from
		idBuf = idTemplate.appendTo(idBuf[:0], seq, "")
		renderCount.Add(1)
		if deny != nil && deny.Match(idBuf) {
			return fmt.Errorf("client ID %q matches safety.clientIdDenyRegex", idBuf)
		}
		if len(prefixes) == 0 || wi >= len(s.Workloads) || topics[wi] == nil {
			continue
		}
		topicBuf = topics[wi].appendTo(topicBuf[:0], seq, string(idBuf))
		if p, ok := overlapsAny(string(topicBuf), prefixes); ok {
			return fmt.Errorf("workload %q topic %q (client ID %q) matches forbidden prefix %q", s.Workloads[wi].Name, topicBuf, idBuf, p)
		}
	}
	return nil
}

// idleWarnings flags workloads that send nothing for at least the broker's
// idle timeout hint while keepAlive would not either.
func (s Scenario) idleWarnings() []string {
	hint := s.Broker.IdleTimeoutHint.Duration
	if hint <= 0 {
		return nil
	}
	keep := s.Broker.KeepAlive.Duration
	if keep <= 0 {
		keep = 30 * time.Second
	}
	if keep < hint {
		return nil
	}
	var out []string
	for _, w := range s.Workloads {
		silent := w.Type == "connection" || w.Type == "subscriber"
		if w.Type == "publisher" || w.Type == "mixed" {
			silent = w.RatePerClient <= 0 || math.IsNaN(w.RatePerClient) || time.Duration(float64(time.Second)/w.RatePerClient) >= hint
		}
		if silent {
			out = append(out, fmt.Sprintf("workload %q sends nothing for at least %s: keepAlive %s >= broker.idleTimeoutHint %s, so the load balancer may drop idle connections; set keepAlive <= %s", w.Name, hint, keep, hint, hint/2))
		}
	}
	return out
}

// portWarning flags more clients per worker than the source ports available
// towards the broker addresses.
func (s Scenario) portWarning(peak int, o PlanOptions) string {
	workers := max(o.Workers, 1)
	dest := o.Destinations
	if dest <= 0 {
		dest = max(len(s.Broker.AddressLabels), 1)
	}
	per := (peak + workers - 1) / workers
	if per <= maxSourcePorts*dest {
		return ""
	}
	noun := "addresses"
	if dest == 1 {
		noun = "address"
	}
	return fmt.Sprintf("%d clients per worker exceed ~%d source ports x %d destination %s; add workers or broker addresses", per, maxSourcePorts, dest, noun)
}
