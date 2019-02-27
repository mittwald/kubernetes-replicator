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

// Replicator describes the common interface that the secret and configmap
// replicators shoud adhere to
type Replicator interface {
	Run()
	Synced() bool
}
