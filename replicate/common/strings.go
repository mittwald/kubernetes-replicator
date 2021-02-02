package common

import (
	"fmt"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	"regexp"
	"sort"
	"strings"
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

	if oma, ok := obj.(metav1.ObjectMetaAccessor); ok {
		return oma.GetObjectMeta()
	} else if o, ok := obj.(metav1.Object); ok {
		return o
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

// GetDifferenceBetweenStringLists returns any entry that is in list a, but not in list b
func GetDifferenceBetweenStringLists(a, b []string) []string {
	if len(b) == 0 {
		return a
	}

	if len(a) == 0 {
		return []string{}
	}

	compare := make(map[string]struct{}, len(b))
	for _, entry := range b {
		compare[entry] = struct{}{}
	}
	var diff []string
	for _, entry := range a {
		if _, found := compare[entry]; !found {
			diff = append(diff, entry)
		}
	}

	return diff
}