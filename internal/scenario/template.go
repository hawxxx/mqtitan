package scenario

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var sequencePattern = regexp.MustCompile(`\$\{sequence(?::(\d+))?\}`)

// templateVariable stands in for any ${...} payload variable. Validation replaces
// each with 0, which is valid both inside a JSON string and as a bare number, so
// numeric variables such as ${random.float:1:9} may appear unquoted.
var templateVariable = regexp.MustCompile(`\$\{[^}]*\}`)

func Expand(template string, sequence int, clientID string) string {
	out := sequencePattern.ReplaceAllStringFunc(template, func(match string) string {
		parts := sequencePattern.FindStringSubmatch(match)
		if parts[1] == "" {
			return strconv.Itoa(sequence)
		}
		width, _ := strconv.Atoi(parts[1])
		return fmt.Sprintf("%0*d", width, sequence)
	})
	return strings.ReplaceAll(out, "${clientId}", clientID)
}

// renderer is a template compiled once for rendering many sequences. It gives
// the same result as Expand followed by replacing ${worker}.
type renderer struct {
	parts []renderPart
}

type renderKind uint8

const (
	partLiteral renderKind = iota
	partSequence
	partClientID
)

type renderPart struct {
	kind  renderKind
	text  string
	width int
}

func compileRenderer(template, worker string) *renderer {
	r := &renderer{}
	literal := func(text string) {
		for text != "" {
			before, after, found := strings.Cut(text, "${clientId}")
			if before != "" {
				r.parts = append(r.parts, renderPart{kind: partLiteral, text: strings.ReplaceAll(before, "${worker}", worker)})
			}
			if found {
				r.parts = append(r.parts, renderPart{kind: partClientID})
			}
			text = after
		}
	}
	last := 0
	for _, m := range sequencePattern.FindAllStringSubmatchIndex(template, -1) {
		literal(template[last:m[0]])
		width := 0
		if m[2] >= 0 {
			width, _ = strconv.Atoi(template[m[2]:m[3]])
		}
		r.parts = append(r.parts, renderPart{kind: partSequence, width: width})
		last = m[1]
	}
	literal(template[last:])
	return r
}

// appendTo appends the rendering of sequence and clientID to dst.
func (r *renderer) appendTo(dst []byte, sequence int, clientID string) []byte {
	for _, p := range r.parts {
		switch p.kind {
		case partLiteral:
			dst = append(dst, p.text...)
		case partClientID:
			dst = append(dst, clientID...)
		case partSequence:
			n := len(dst)
			dst = strconv.AppendInt(dst, int64(sequence), 10)
			if digits := len(dst) - n; digits < p.width {
				pad := p.width - digits
				dst = append(dst, make([]byte, pad)...)
				copy(dst[n+pad:], dst[n:n+digits])
				for i := 0; i < pad; i++ {
					dst[n+i] = '0'
				}
			}
		}
	}
	return dst
}
