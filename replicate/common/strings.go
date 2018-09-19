package common

import (
	"fmt"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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

// Create a key from Kubernetes resource
func MustGetKey(obj interface{}) string {
	if obj == nil {
		return ""
	} else if s, ok := obj.(*metav1.ObjectMeta); ok {
		return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
	} else if s, ok := obj.(metav1.ObjectMeta); ok {
		return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
	} else if s, ok := obj.(*v1.Secret); ok {
		return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
	} else if s, ok := obj.(v1.Secret); ok {
		return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
	} else if s, ok := obj.(*v1.ConfigMap); ok {
		return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
	} else if s, ok := obj.(v1.ConfigMap); ok {
		return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
	} else if s, ok := obj.(*rbacv1.Role); ok {
		return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
	} else if s, ok := obj.(rbacv1.Role); ok {
		return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
	} else if s, ok := obj.(*rbacv1.RoleBinding); ok {
		return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
	} else if s, ok := obj.(rbacv1.RoleBinding); ok {
		return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
	} else if s, ok := obj.(*v1.Namespace); ok {
		return fmt.Sprintf("%s", s.Namespace)
	} else if s, ok := obj.(v1.Namespace); ok {
		return fmt.Sprintf("%s", s.Namespace)
	} else {
		panic(fmt.Sprintf("Unknown type: %s", reflect.TypeOf(obj)))
	}
}

func MustGetObjectMeta(obj interface{}) *metav1.ObjectMeta {
	if obj == nil {
		return nil
	} else if s, ok := obj.(*metav1.ObjectMeta); ok {
		return s
	} else if s, ok := obj.(metav1.ObjectMeta); ok {
		return &s
	} else if s, ok := obj.(*v1.Secret); ok {
		return &s.ObjectMeta
	} else if s, ok := obj.(v1.Secret); ok {
		return &s.ObjectMeta
	} else if s, ok := obj.(*v1.ConfigMap); ok {
		return &s.ObjectMeta
	} else if s, ok := obj.(v1.ConfigMap); ok {
		return &s.ObjectMeta
	} else if s, ok := obj.(*rbacv1.Role); ok {
		return &s.ObjectMeta
	} else if s, ok := obj.(rbacv1.Role); ok {
		return &s.ObjectMeta
	} else if s, ok := obj.(*rbacv1.RoleBinding); ok {
		return &s.ObjectMeta
	} else if s, ok := obj.(rbacv1.RoleBinding); ok {
		return &s.ObjectMeta
	} else if s, ok := obj.(*v1.Namespace); ok {
		return &s.ObjectMeta
	} else if s, ok := obj.(v1.Namespace); ok {
		return &s.ObjectMeta
	} else {
		panic(fmt.Sprintf("Unknown type: %s", reflect.TypeOf(obj)))
	}

}
