package replicate

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type replicatorProps struct {
	client     kubernetes.Interface
	store      cache.Store
	controller cache.Controller

	dependencyMap map[string][]string
}
