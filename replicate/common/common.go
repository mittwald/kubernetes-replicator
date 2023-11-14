package common

import (
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

func BuildStrictRegex(regex string) string {
	reg := strings.TrimSpace(regex)
	if !strings.HasPrefix(reg, "^") {
		reg = "^" + reg
	}
	if !strings.HasSuffix(reg, "$") {
		reg = reg + "$"
	}
	return reg
}

func JSONPatchPathEscape(annotation string) string {
	return strings.ReplaceAll(annotation, "/", "~1")
}

type Annotatable interface {
	GetAnnotations() map[string]string
	SetAnnotations(map[string]string)
}

func CopyAnnotations[I, O Annotatable](input I, output O) {
	val := input.GetAnnotations()
	copy := make(map[string]string, len(val))

	strip, ok := val[StripAnnotations]
	if !ok || strings.ToLower(strip) != "true" {
		for k, v := range val {
			if strings.HasPrefix(k, Prefix) {
				continue
			}
			copy[k] = v
		}

		output.SetAnnotations(copy)
	}
}
