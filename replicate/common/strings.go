package common

import (
	"fmt"
	"reflect"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func GetKeysFromBinaryMap(data map[string][]byte) []string {
	strings := make([]string, 0)
	for k := range data {
		strings = append(strings, k)
	}
	sort.Strings(strings)

	return strings
}

func GetKeysFromStringMap(data map[string]string) []string {
	strings := make([]string, 0)
	for k := range data {
		strings = append(strings, k)
	}
	sort.Strings(strings)

	return strings
}

// MustGetKey creates a key from Kubernetes resource in the format <namespace>/<name>
func MustGetKey(obj interface{}) string {
	if obj == nil {
		return ""
	}

	o := MustGetObject(obj)
	return fmt.Sprintf("%s/%s", o.GetNamespace(), o.GetName())

}

// MustGetObject casts the object into a Kubernetes `metav1.Object`
func MustGetObject(obj interface{}) metav1.Object {
	if obj == nil {
		return nil
	}

	switch o := obj.(type) {
	case metav1.ObjectMetaAccessor:
		return o.GetObjectMeta()
	case metav1.Object:
		return o
	case cache.DeletedFinalStateUnknown:
		return MustGetObject(o.Obj)
	}

	panic(errors.Errorf("Unknown type: %v", reflect.TypeOf(obj)))
}

// Limits for user supplied, comma separated lists of namespace patterns (for example the "replicate-to" and
// "replication-allowed-namespaces" annotations). Annotations are controlled by anyone who may create the annotated
// object, so the amount of work spent on compiling them has to be bounded.
const (
	// MaxNamespacePatternListLength is the maximum length (in bytes) of a pattern list.
	MaxNamespacePatternListLength = 16 * 1024
	// MaxNamespacePatterns is the maximum number of patterns in a pattern list.
	MaxNamespacePatterns = 256
	// MaxNamespacePatternListCost is the maximum estimated size (roughly the number of instructions of the compiled
	// programs) of all patterns in a pattern list.
	MaxNamespacePatternListCost = 64 * 1024
)

// ParseNamespacePatternList splits a comma separated list of namespace patterns and compiles each pattern with
// CompileStrictRegex. Empty entries are skipped. Lists that exceed the limits above are rejected as a whole. Invalid
// patterns are skipped and reported through the returned error, together with the valid patterns.
func ParseNamespacePatternList(list string) ([]*regexp.Regexp, error) {
	if len(list) > MaxNamespacePatternListLength {
		return nil, errors.Errorf("namespace pattern list is %d bytes long, the maximum is %d bytes",
			len(list), MaxNamespacePatternListLength)
	}

	patterns := make([]string, 0)
	for _, s := range strings.Split(list, ",") {
		if s = strings.TrimSpace(s); s != "" {
			patterns = append(patterns, s)
		}
	}
	if len(patterns) > MaxNamespacePatterns {
		return nil, errors.Errorf("namespace pattern list contains %d patterns, the maximum is %d",
			len(patterns), MaxNamespacePatterns)
	}

	totalCost := 0
	invalid := make([]string, 0)
	parsed := make([]string, 0, len(patterns))
	for _, s := range patterns {
		tree, err := syntax.Parse(s, syntax.Perl)
		if err != nil {
			invalid = append(invalid, s)
			continue
		}
		totalCost += regexCost(tree)
		if totalCost > MaxNamespacePatternListCost {
			return nil, errors.Errorf("namespace pattern list is too complex, the maximum estimated cost is %d",
				MaxNamespacePatternListCost)
		}
		parsed = append(parsed, s)
	}

	result := make([]*regexp.Regexp, 0, len(parsed))
	for _, s := range parsed {
		r, err := CompileStrictRegex(s)
		if err != nil {
			invalid = append(invalid, s)
			continue
		}
		result = append(result, r)
	}

	if len(invalid) > 0 {
		return result, errors.Errorf("ignoring %d invalid namespace pattern(s), first one: %q",
			len(invalid), truncate(invalid[0], 100))
	}

	return result, nil
}

// StringToPatternList compiles a comma separated list of namespace patterns. Invalid patterns are logged and
// skipped; a list that exceeds the limits of ParseNamespacePatternList yields no patterns at all.
func StringToPatternList(list string) []*regexp.Regexp {
	result, err := ParseNamespacePatternList(list)
	if err != nil {
		log.WithError(err).Error("invalid namespace pattern list")
	}

	return result
}

// MatchesAnyPattern returns true if name matches one of the given patterns.
func MatchesAnyPattern(patterns []*regexp.Regexp, name string) bool {
	for _, p := range patterns {
		if p.MatchString(name) {
			return true
		}
	}
	return false
}

// regexCost estimates the size of the program that a parsed regular expression compiles to. Counted repetitions
// are multiplied out and character classes are weighted by their number of ranges, as that is what makes compiling
// expressions like "[\pL]{999}" expensive.
func regexCost(re *syntax.Regexp) int {
	cost := 0
	switch re.Op {
	case syntax.OpCharClass:
		cost = 1 + len(re.Rune)/2
	case syntax.OpLiteral:
		cost = len(re.Rune)
	case syntax.OpRepeat:
		n := re.Max
		if n < 0 {
			n = re.Min + 1
		}
		if n < 1 {
			n = 1
		}
		cost = n * regexCost(re.Sub[0])
	default:
		cost = 1
		for _, sub := range re.Sub {
			cost += regexCost(sub)
		}
	}

	if cost > MaxNamespacePatternListCost {
		// avoid overflows for deeply nested repetitions; anything above the limit is rejected anyway
		return MaxNamespacePatternListCost + 1
	}
	return cost
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
