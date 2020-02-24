package replicate

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type configMapReplicator struct {
	replicatorProps
}

// NewConfigMapReplicator creates a new config map replicator
func NewConfigMapReplicator(client kubernetes.Interface, resyncPeriod time.Duration, allowAll bool) Replicator {
	repl := configMapReplicator{
		replicatorProps: replicatorProps{
			allowAll:      allowAll,
			client:        client,
			dependencyMap: make(map[string]map[string]interface{}),
		},
	}

	store, controller := cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				return client.CoreV1().ConfigMaps("").List(lo)
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				return client.CoreV1().ConfigMaps("").Watch(lo)
			},
		},
		&v1.ConfigMap{},
		resyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    repl.ConfigMapAdded,
			UpdateFunc: func(old interface{}, new interface{}) { repl.ConfigMapAdded(new) },
			DeleteFunc: repl.ConfigMapDeleted,
		},
	)

	repl.store = store
	repl.controller = controller

	return &repl
}

func (r *configMapReplicator) Synced() bool {
	return r.controller.HasSynced()
}

func (r *configMapReplicator) Run() {
	log.Printf("running config map controller")
	r.controller.Run(wait.NeverStop)
}

func (r *configMapReplicator) ConfigMapAdded(obj interface{}) {
	configMap := obj.(*v1.ConfigMap)
	configMapKey := fmt.Sprintf("%s/%s", configMap.Namespace, configMap.Name)

	replicas, ok := r.dependencyMap[configMapKey]
	if ok {
		log.Printf("config map %s has %d dependents", configMapKey, len(replicas))
		r.updateDependents(configMap, replicas)
	}

	val, ok := configMap.Annotations[ReplicateFromAnnotation]
	if !ok {
		return
	}

	log.Printf("config map %s/%s is replicated from %s", configMap.Namespace, configMap.Name, val)
	v := strings.SplitN(val, "/", 2)

	if len(v) < 2 {
		return
	}

	if _, ok := r.dependencyMap[val]; !ok {
		r.dependencyMap[val] = make(map[string]interface{})
	}

	r.dependencyMap[val][configMapKey] = nil

	sourceObject, exists, err := r.store.GetByKey(val)
	if err != nil {
		log.Printf("could not get config map %s: %s", val, err)
		return
	} else if !exists {
		log.Printf("could not get config map %s: does not exist", val)
		return
	}

	sourceConfigMap := sourceObject.(*v1.ConfigMap)

	r.replicateConfigMap(configMap, sourceConfigMap)
}

func (r *configMapReplicator) replicateConfigMap(configMap *v1.ConfigMap, sourceConfigMap *v1.ConfigMap) error {
	// make sure replication is allowed
	if ok, err := r.isReplicationPermitted(&configMap.ObjectMeta, &sourceConfigMap.ObjectMeta); !ok {
		log.Printf("replication of config map %s/%s is not permitted: %s", sourceConfigMap.Namespace, sourceConfigMap.Name, err)
		return err
	}

	targetVersion, ok := configMap.Annotations[ReplicatedFromVersionAnnotation]
	sourceVersion := sourceConfigMap.ResourceVersion

	if ok && targetVersion == sourceVersion {
		log.Printf("config map %s/%s is already up-to-date", configMap.Namespace, configMap.Name)
		return nil
	}

	configMapCopy := configMap.DeepCopy()

	if configMapCopy.Data == nil {
		configMapCopy.Data = make(map[string]string)
	}

	for key, value := range sourceConfigMap.Data {
		configMapCopy.Data[key] = value
	}

	if sourceConfigMap.BinaryData != nil {
		configMapCopy.BinaryData = make(map[string][]byte)
		for key, value := range sourceConfigMap.BinaryData {
			configMapCopy.BinaryData[key] = value
		}
	}

	log.Printf("updating config map %s/%s", configMap.Namespace, configMap.Name)

	configMapCopy.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	configMapCopy.Annotations[ReplicatedFromVersionAnnotation] = sourceConfigMap.ResourceVersion

	s, err := r.client.CoreV1().ConfigMaps(configMap.Namespace).Update(configMapCopy)
	if err != nil {
		return err
	}

	r.store.Update(s)
	return nil
}

func (r *configMapReplicator) configMapFromStore(key string) (*v1.ConfigMap, error) {
	obj, exists, err := r.store.GetByKey(key)
	if err != nil {
		return nil, fmt.Errorf("could not get config map %s: %s", key, err)
	}

	if !exists {
		return nil, fmt.Errorf("could not get config map %s: does not exist", key)
	}

	configMap, ok := obj.(*v1.ConfigMap)
	if !ok {
		return nil, fmt.Errorf("bad type returned from store: %T", obj)
	}

	return configMap, nil
}

func (r *configMapReplicator) updateDependents(configMap *v1.ConfigMap, dependents map[string]interface{}) error {
	for dependentKey := range dependents {
		log.Printf("updating dependent config map %s/%s -> %s", configMap.Namespace, configMap.Name, dependentKey)

		targetObject, exists, err := r.store.GetByKey(dependentKey)
		if err != nil {
			log.Printf("could not get dependent config map %s: %s", dependentKey, err)
			continue
		} else if !exists {
			log.Printf("could not get dependent config map %s: does not exist", dependentKey)
			continue
		}

		targetConfigMap := targetObject.(*v1.ConfigMap)

		r.replicateConfigMap(targetConfigMap, configMap)
	}

	return nil
}

func (r *configMapReplicator) ConfigMapDeleted(obj interface{}) {
	configMap := obj.(*v1.ConfigMap)
	configMapKey := fmt.Sprintf("%s/%s", configMap.Namespace, configMap.Name)

	replicas, ok := r.dependencyMap[configMapKey]
	if !ok {
		log.Printf("config map %s has no dependents and can be deleted without issues", configMapKey)
		return
	}

	for dependentKey := range replicas {
		targetConfigMap, err := r.configMapFromStore(dependentKey)
		if err != nil {
			log.Printf("could not load dependent config map: %s", err)
			continue
		}

		patch := []JSONPatchOperation{{Operation: "remove", Path: "/data"}}
		patchBody, err := json.Marshal(&patch)

		if err != nil {
			log.Printf("error while building patch body for config map %s: %s", dependentKey, err)
			continue
		}

		log.Printf("clearing dependent config map %s", dependentKey)
		log.Printf("patch body: %s", string(patchBody))

		s, err := r.client.CoreV1().ConfigMaps(targetConfigMap.Namespace).Patch(targetConfigMap.Name, types.JSONPatchType, patchBody)
		if err != nil {
			log.Printf("error while patching config map %s: %s", dependentKey, err)
			continue
		}

		r.store.Update(s)
	}
}
