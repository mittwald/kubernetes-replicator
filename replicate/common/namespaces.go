package common

import (
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"sync"
	"time"
)

var namespaceWatcher NamespaceWatcher

type AddFunc func(obj *v1.Namespace)

type NamespaceWatcher struct {
	doOnce sync.Once

	NamespaceStore      cache.Store
	NamespaceController cache.Controller

	AddFuncs []AddFunc
}

// create will create a new namespace if one does not already exist. If it does, it will do nothing.
func (nw *NamespaceWatcher) create(client kubernetes.Interface, resyncPeriod time.Duration) {
	nw.doOnce.Do(func() {
		namespaceAdded := func(obj interface{}) {
			namespace := obj.(*v1.Namespace)
			for _, addFunc := range nw.AddFuncs {
				go addFunc(namespace)
			}
		}

		nw.NamespaceStore, nw.NamespaceController = cache.NewInformer(
			&cache.ListWatch{
				ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
					return client.CoreV1().Namespaces().List(lo)
				},
				WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
					return client.CoreV1().Namespaces().Watch(lo)
				},
			},
			&v1.Namespace{},
			resyncPeriod,
			cache.ResourceEventHandlerFuncs{
				AddFunc: namespaceAdded,
			},
		)

		log.WithField("kind", "Namespace").Infof("running Namespace controller")
		go nw.NamespaceController.Run(wait.NeverStop)

	})
}

// OnNamespaceAdded will add another method to a list of functions to be called when a new namespace is created
func (nw *NamespaceWatcher) OnNamespaceAdded(client kubernetes.Interface, resyncPeriod time.Duration, addFunc AddFunc) {
	nw.create(client, resyncPeriod)
	nw.AddFuncs = append(nw.AddFuncs, addFunc)
}
