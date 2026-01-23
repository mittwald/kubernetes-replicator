package common

import (
	"regexp"
)

type AnnotationsFilter struct {
	ExcludePatterns []string
	compiled        []*regexp.Regexp
}

func NewAnnotationsFilter(patterns []string) *AnnotationsFilter {
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
	return &AnnotationsFilter{
		ExcludePatterns: patterns,
		compiled:        compiled,
	}
}

func (f *AnnotationsFilter) ShouldExclude(annotations map[string]string) bool {
	for _, re := range f.compiled {
		for annotation := range annotations {
			if re.MatchString(annotation) {
				return true
			}
		}
	}
	return false
}
