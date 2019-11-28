package replicate

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
			dependencyMap: make(map[string][]string),
			targetMap: make(map[string]string),
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

	if val, ok := r.targetMap[configMapKey]; ok {
		if annotation, ok := resolveAnnotation(&configMap.ObjectMeta, ReplicateToAnnotation); !ok || val != annotation {
			log.Printf("annotation of source config map %s changed", configMapKey)

			r.deleteConfigMap(val, configMap)
			delete(r.targetMap, configMapKey)
		}
	}

	if replicas, ok := r.dependencyMap[configMapKey]; ok {
		log.Printf("config map %s has %d dependents", configMapKey, len(replicas))
		r.updateDependents(configMap, replicas)
	}

	if val, ok := configMap.Annotations[ReplicatedByAnnotation]; ok {
		var sourceConfigMap *v1.ConfigMap = nil

		sourceObject, exists, err := r.store.GetByKey(val)
		if err != nil {
			log.Printf("could not get config map %s: %s", val, err)
			return

		} else if !exists {
			log.Printf("source config map %s deleted", val)

		} else {
			sourceConfigMap = sourceObject.(*v1.ConfigMap)

			if !annotationRefersTo(&sourceConfigMap.ObjectMeta, ReplicateToAnnotation, &configMap.ObjectMeta) {
				log.Printf("annotation of source config map %s changed", val)
				sourceConfigMap = nil
			}
		}

		if sourceConfigMap == nil {
			r.doDeleteConfigMap(configMap)
			return

		} else {
			r.installConfigMap("", configMap, sourceConfigMap)
			return
		}
	}

	if val, ok := resolveAnnotation(&configMap.ObjectMeta, ReplicateFromAnnotation); ok {
		log.Printf("config map %s is replicated from %s", configMapKey, val)

		if _, ok := r.dependencyMap[val]; !ok {
			r.dependencyMap[val] = make([]string, 0, 1)
		}
		r.dependencyMap[val] = append(r.dependencyMap[val], configMapKey)

		if sourceObject, exists, err := r.store.GetByKey(val); err != nil {
			log.Printf("could not get config map %s: %s", val, err)
			return

		} else if !exists {
			log.Printf("source config map %s deleted", val)
			r.doClearConfigMap(configMap)
			return

		} else {
			sourceConfigMap := sourceObject.(*v1.ConfigMap)
			r.replicateConfigMap(configMap, sourceConfigMap)
			return
		}
	}

	if val, ok := resolveAnnotation(&configMap.ObjectMeta, ReplicateToAnnotation); ok {
		log.Printf("config map %s is replicated to %s", configMapKey, val)

		r.targetMap[configMapKey] = val

		r.installConfigMap(val, nil, configMap)
		return
	}
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

	if sourceConfigMap.Data != nil {
		configMapCopy.Data = make(map[string]string)
		for key, value := range sourceConfigMap.Data {
			configMapCopy.Data[key] = value
		}
	} else {
		configMapCopy.Data = nil
	}

	if sourceConfigMap.BinaryData != nil {
		configMapCopy.BinaryData = make(map[string][]byte)
		for key, value := range sourceConfigMap.BinaryData {
			newValue := make([]byte, len(value))
			copy(newValue, value)
			configMapCopy.BinaryData[key] = newValue
		}
	} else {
		configMapCopy.BinaryData = nil
	}

	log.Printf("updating config map %s/%s", configMap.Namespace, configMap.Name)

	configMapCopy.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	configMapCopy.Annotations[ReplicatedFromVersionAnnotation] = sourceConfigMap.ResourceVersion

	s, err := r.client.CoreV1().ConfigMaps(configMap.Namespace).Update(configMapCopy)
	if err != nil {
		log.Printf("error while updating config map %s/%s: %s", configMap.Namespace, configMap.Name, err)
		return err
	}

	r.store.Update(s)
	return nil
}

func (r *configMapReplicator) installConfigMap(target string, targetConfigMap *v1.ConfigMap, sourceConfigMap *v1.ConfigMap) error {
	var targetSplit []string
	if targetConfigMap == nil {
		targetSplit = strings.SplitN(target, "/", 2)

		if len(targetSplit) != 2 {
			err := fmt.Errorf("illformed annotation %s: expected namespace/name, got %s",
				ReplicatedByAnnotation, target)
			log.Printf("%s", err)
			return err
		}

		if targetObject, exists, err := r.store.GetByKey(target); err != nil {
			log.Printf("could not get config map %s: %s", target, err)
			return err

		} else if exists {
			targetConfigMap = targetObject.(*v1.ConfigMap)
		}
	} else {
		targetSplit = []string{targetConfigMap.Namespace, targetConfigMap.Name}
	}

	if targetConfigMap != nil {
		if verion, ok := targetConfigMap.Annotations[ReplicatedFromVersionAnnotation]; ok && verion == sourceConfigMap.ResourceVersion {
			log.Printf("config map %s/%s is already up-to-date", targetConfigMap.Namespace, targetConfigMap.Name)
			return nil
		// make sure replication is allowed
		} else if ok, err := r.canReplicateTo(&sourceConfigMap.ObjectMeta, &targetConfigMap.ObjectMeta); !ok {
			log.Printf("config map %s/%s cannot be replicated to %s/%s: %s",
				sourceConfigMap.Namespace, sourceConfigMap.Name, targetConfigMap.Namespace, targetConfigMap.Name, err)
			return err
		}
	}

	configMapCopy := v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       sourceConfigMap.Kind,
			APIVersion: sourceConfigMap.APIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   targetSplit[0],
			Name:        targetSplit[1],
			Annotations: map[string]string{},
		},
	}

	if sourceConfigMap.Data != nil {
		configMapCopy.Data = make(map[string]string)
		for key, value := range sourceConfigMap.Data {
			configMapCopy.Data[key] = value
		}
	}

	if sourceConfigMap.BinaryData != nil {
		configMapCopy.BinaryData = make(map[string][]byte)
		for key, value := range sourceConfigMap.BinaryData {
			newValue := make([]byte, len(value))
			copy(newValue, value)
			configMapCopy.BinaryData[key] = newValue
		}
	}

	log.Printf("installing config map %s/%s", configMapCopy.Namespace, configMapCopy.Name)

	configMapCopy.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	configMapCopy.Annotations[ReplicatedByAnnotation] = fmt.Sprintf("%s/%s",
		sourceConfigMap.Namespace, sourceConfigMap.Name)
	configMapCopy.Annotations[ReplicatedFromVersionAnnotation] = sourceConfigMap.ResourceVersion

	var s *v1.ConfigMap
	var err error
	if targetConfigMap == nil {
		s, err = r.client.CoreV1().ConfigMaps(configMapCopy.Namespace).Create(&configMapCopy)
	} else {
		configMapCopy.ResourceVersion = targetConfigMap.ResourceVersion
		s, err = r.client.CoreV1().ConfigMaps(configMapCopy.Namespace).Update(&configMapCopy)
	}

	if err != nil {
		log.Printf("error while installing config map %s/%s: %s", configMapCopy.Namespace, configMapCopy.Name, err)
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

func (r *configMapReplicator) updateDependents(configMap *v1.ConfigMap, replicas []string) error {
	configMapKey := fmt.Sprintf("%s/%s", configMap.Namespace, configMap.Name)

	sort.Strings(replicas)
	updatedReplicas := make([]string, 0, 0)
	var previous string

	for _, dependentKey := range replicas {
		// get rid of dupplicates in replicas
		if previous == dependentKey {
			continue
		}
		previous = dependentKey

		targetConfigMap, err := r.configMapFromStore(dependentKey)
		if err != nil {
			log.Printf("could not load dependent config map: %s", err)
			continue
		}

		val, ok := resolveAnnotation(&targetConfigMap.ObjectMeta, ReplicateFromAnnotation)
		if !ok || val != configMapKey {
			log.Printf("annotation of dependent config map %s changed", dependentKey)
			continue
		}

		updatedReplicas = append(updatedReplicas, dependentKey)

		r.replicateConfigMap(targetConfigMap, configMap)
	}

	if len(updatedReplicas) > 0 {
		r.dependencyMap[configMapKey] = updatedReplicas
	} else {
		delete(r.dependencyMap, configMapKey)
	}

	return nil
}

func (r *configMapReplicator) ConfigMapDeleted(obj interface{}) {
	configMap := obj.(*v1.ConfigMap)
	configMapKey := fmt.Sprintf("%s/%s", configMap.Namespace, configMap.Name)

	if val, ok := r.targetMap[configMapKey]; ok {
		r.deleteConfigMap(val, configMap)
		delete(r.targetMap, configMapKey)
	}

	replicas, ok := r.dependencyMap[configMapKey]
	if !ok {
		return
	}

	sort.Strings(replicas)
	updatedReplicas := make([]string, 0, 0)
	var previous string

	for _, dependentKey := range replicas {
		// get rid of dupplicates in replicas
		if previous == dependentKey {
			continue
		}
		previous = dependentKey

		if ok, _ := r.clearConfigMap(dependentKey, configMap); ok {
			updatedReplicas = append(updatedReplicas, dependentKey)
		}
	}

	if len(updatedReplicas) > 0 {
		r.dependencyMap[configMapKey] = updatedReplicas
	} else {
		delete(r.dependencyMap, configMapKey)
	}
}

func (r *configMapReplicator) clearConfigMap(configMapKey string, sourceConfigMap *v1.ConfigMap) (bool, error) {
	targetConfigMap, err := r.configMapFromStore(configMapKey)
	if err != nil {
		log.Printf("could not load dependent config map: %s", err)
		return false, err
	}

	if !annotationRefersTo(&targetConfigMap.ObjectMeta, ReplicateFromAnnotation, &sourceConfigMap.ObjectMeta) {
		log.Printf("annotation of dependent config map %s changed", configMapKey)
		return false, nil
	}

	return true, r.doClearConfigMap(targetConfigMap)
}

func (r *configMapReplicator) doClearConfigMap(configMap *v1.ConfigMap) error {
	if _, ok := configMap.Annotations[ReplicatedFromVersionAnnotation]; !ok {
		log.Printf("config map %s/%s is already up-to-date", configMap.Namespace, configMap.Name)
		return nil
	}

	configMapCopy := configMap.DeepCopy()
	configMapCopy.Data = nil
	configMapCopy.BinaryData = nil

	log.Printf("clearing config map %s/%s", configMap.Namespace, configMap.Name)

	configMapCopy.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	delete(configMapCopy.Annotations, ReplicatedFromVersionAnnotation)

	s, err := r.client.CoreV1().ConfigMaps(configMap.Namespace).Update(configMapCopy)
	if err != nil {
		log.Printf("error while clearing config map %s/%s", configMap.Namespace, configMap.Name)
		return err
	}

	r.store.Update(s)
	return nil
}

func (r *configMapReplicator) deleteConfigMap(configMapKey string, sourceConfigMap *v1.ConfigMap) (bool, error) {
	object, exists, err := r.store.GetByKey(configMapKey)

	if err != nil {
		log.Printf("could not get config map %s: %s", configMapKey, err)
		return false, err

	} else if !exists {
		log.Printf("could not get config map %s: does not exist", configMapKey)
		return false, nil
	// make sure replication is allowed
	}

	configMap := object.(*v1.ConfigMap)

	if ok, err := r.canReplicateTo(&sourceConfigMap.ObjectMeta, &configMap.ObjectMeta); !ok {
		log.Printf("config map %s was not created by replication: %s", configMapKey, err)
		return false, nil
	// delete the config map
	} else {
		return true, r.doDeleteConfigMap(configMap)
	}
}

func (r *configMapReplicator) doDeleteConfigMap(configMap *v1.ConfigMap) error {
	log.Printf("deleting config map %s/%s", configMap.Namespace, configMap.Name)

	options := metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			ResourceVersion: &configMap.ResourceVersion,
		},
	}

	err := r.client.CoreV1().ConfigMaps(configMap.Namespace).Delete(configMap.Name, &options)
	if err != nil {
		log.Printf("error while deleting config map %s/%s: %s", configMap.Namespace, configMap.Name, err)
		return err
	}

	r.store.Delete(configMap)
	return nil
}

