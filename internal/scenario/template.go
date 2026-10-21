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
