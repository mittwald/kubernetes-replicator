package common

import (
	"regexp"
)

type NamespaceFilter struct {
	ExcludePatterns []string
	compiled        []*regexp.Regexp
}

func NewNamespaceFilter(patterns []string) *NamespaceFilter {
	var compiled []*regexp.Regexp
	for _, pat := range patterns {
		if pat == "" {
			continue
		}
		re, err := regexp.Compile(pat)
		if err == nil {
			compiled = append(compiled, re)
		}
	}
	return &NamespaceFilter{
		ExcludePatterns: patterns,
		compiled:        compiled,
	}
}

func (f *NamespaceFilter) ShouldExclude(namespace string) bool {
	for _, re := range f.compiled {
		if re.MatchString(namespace) {
			return true
		}
	}
	return false
}
