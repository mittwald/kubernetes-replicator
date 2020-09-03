package common

import (
	"fmt"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	"sort"
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
