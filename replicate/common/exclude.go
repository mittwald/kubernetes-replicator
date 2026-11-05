package common

import (
	"regexp"
	"strings"

	"github.com/pkg/errors"
)

// NamespaceFilter decides which namespaces are excluded from replication. Excluded namespaces are never written to:
// objects are neither pushed into them, nor are pull targets in them updated, nor are objects in them deleted or
// patched by the replicator.
type NamespaceFilter struct {
	ExcludePatterns []string
	compiled        []*regexp.Regexp
}

// NewNamespaceFilter creates a filter from a list of regular expressions. As before, the expressions are not
// anchored, i.e. a namespace is excluded if any part of its name matches. Surrounding white space is ignored. An
// error is returned if a pattern is not a valid regular expression, so that a typo cannot silently disable the
// protection.
func NewNamespaceFilter(patterns []string) (*NamespaceFilter, error) {
	var compiled []*regexp.Regexp
	var trimmed []string
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid namespace exclude pattern %q", pat)
		}
		compiled = append(compiled, re)
		trimmed = append(trimmed, pat)
	}
	return &NamespaceFilter{
		ExcludePatterns: trimmed,
		compiled:        compiled,
	}, nil
}

// ShouldExclude returns true if the namespace must not be touched by the replicator. It is safe to call on a nil
// filter.
func (f *NamespaceFilter) ShouldExclude(namespace string) bool {
	if f == nil {
		return false
	}
	for _, re := range f.compiled {
		if re.MatchString(namespace) {
			return true
		}
	}
	return false
}
