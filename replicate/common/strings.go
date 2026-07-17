package common

import (
	"fmt"
	"reflect"
	"regexp"
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

func StringToPatternList(list string) (result []*regexp.Regexp) {
	for _, s := range strings.Split(list, ",") {
		s = BuildStrictRegex(s)
		r, err := regexp.Compile(s)
		if err != nil {
			log.WithError(err).Errorf("Invalid regex '%s' in namespace string %s: %v", s, list, err)
		} else {
			result = append(result, r)
		}
	}

	return
}

// GenerateTargetName creates a target resource name by combining prefix, source name, and suffix
// with implicit dashes. Handles empty prefix/suffix values gracefully and avoids duplicate dashes.
// Validates that the resulting name is a valid Kubernetes resource name.
func GenerateTargetName(sourceName, prefix, suffix string) string {
	var result strings.Builder

	// Add prefix with implicit dash if needed
	if prefix != "" {
		result.WriteString(prefix)
		// Add dash only if prefix doesn't already end with one
		if !strings.HasSuffix(prefix, "-") {
			result.WriteString("-")
		}
	}

	// Add source name
	result.WriteString(sourceName)

	// Add suffix with implicit dash if needed
	if suffix != "" {
		// Add dash only if suffix doesn't start with one
		if !strings.HasPrefix(suffix, "-") {
			result.WriteString("-")
		}
		result.WriteString(suffix)
	}

	targetName := result.String()

	// Validate the resulting name
	if !IsValidKubernetesResourceName(targetName) {
		log.Warnf("Generated target name '%s' may not be valid for Kubernetes resources. "+
			"Source: '%s', Prefix: '%s', Suffix: '%s'", targetName, sourceName, prefix, suffix)
	}

	return targetName
}

// IsValidKubernetesResourceName validates that a name follows Kubernetes naming conventions
func IsValidKubernetesResourceName(name string) bool {
	if name == "" {
		return false
	}

	// Kubernetes resource names must be lowercase alphanumeric or '-'
	// Must start and end with alphanumeric character
	// Must be 253 characters or less
	if len(name) > 253 {
		return false
	}

	// Check if starts and ends with alphanumeric
	if len(name) > 0 {
		first := name[0]
		last := name[len(name)-1]
		if !isAlphanumeric(first) || !isAlphanumeric(last) {
			return false
		}
	}

	// Check all characters are valid
	for _, char := range name {
		if !isAlphanumeric(byte(char)) && char != '-' {
			return false
		}
	}

	return true
}

// isAlphanumeric checks if a byte is a lowercase letter or digit
func isAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}
