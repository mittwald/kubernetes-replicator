package common

import (
	"regexp"
	"strings"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Replicator interface {
	Run()
	Synced() bool
	NamespaceAdded(ns *v1.Namespace)
}

func PreviouslyPresentKeys(object *metav1.ObjectMeta) (map[string]struct{}, bool) {
	keyList, ok := object.Annotations[ReplicatedKeysAnnotation]
	if !ok {
		return nil, false
	}

	keys := strings.Split(keyList, ",")
	out := make(map[string]struct{})

	for _, k := range keys {
		out[k] = struct{}{}
	}

	return out, true
}

// BuildStrictRegex turns a namespace pattern into a regular expression that has to match the whole namespace
// name. The pattern is wrapped into a non-capturing group, so that alternations like "a|b" are anchored as a whole
// (instead of becoming "^a|b$", which matches every name starting with "a" or ending with "b").
//
// Use CompileStrictRegex to compile a pattern; it also verifies that the pattern is a valid expression on its own
// and therefore cannot break out of the group.
func BuildStrictRegex(regex string) string {
	return "^(?:" + strings.TrimSpace(regex) + ")$"
}

// CompileStrictRegex compiles a namespace pattern so that it only matches complete namespace names.
func CompileStrictRegex(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)

	// Validate the pattern on its own first. Otherwise, a pattern like "a)|(.*" would be invalid by itself but
	// become a valid, unanchored expression once wrapped into "^(?:...)$".
	if _, err := regexp.Compile(pattern); err != nil {
		return nil, err
	}

	return regexp.Compile(BuildStrictRegex(pattern))
}

func JSONPatchPathEscape(annotation string) string {
	return strings.ReplaceAll(annotation, "/", "~1")
}
