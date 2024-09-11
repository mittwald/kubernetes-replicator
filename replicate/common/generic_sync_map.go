package common

import (
	"sync"
)

// GenericMap is a generic sync.Map that can store any type of key and value.
type GenericMap[K comparable, V any] struct {
	m sync.Map
}

func (gm *GenericMap[K, V]) Store(key K, value V) {
	gm.m.Store(key, value)
}

func (gm *GenericMap[K, V]) Load(key K) (value V, ok bool) {
	rawValue, ok := gm.m.Load(key)
	if ok {
		value = rawValue.(V)
	}
	return value, ok
}

func (gm *GenericMap[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	rawActual, loaded := gm.m.LoadOrStore(key, value)
	if loaded {
		actual = rawActual.(V)
	} else {
		actual = value
	}
	return actual, loaded
}

func (gm *GenericMap[K, V]) Delete(key K) {
	gm.m.Delete(key)
}

func (gm *GenericMap[K, V]) Range(f func(key K, value V) bool) {
	gm.m.Range(func(rawKey, rawValue any) bool {
		key := rawKey.(K)
		value := rawValue.(V)
		return f(key, value)
	})
}
