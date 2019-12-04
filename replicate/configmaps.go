package replicate

import (
	"fmt"
	"log"
	"time"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

var ConfigMapActions *configMapActions = &configMapActions{}

// NewConfigMapReplicator creates a new config map replicator
func NewConfigMapReplicator(client kubernetes.Interface, resyncPeriod time.Duration, allowAll bool) Replicator {
	repl := objectReplicator{
		replicatorProps: replicatorProps{
			Name:          "config map",
			allowAll:      allowAll,
			client:        client,
			dependencyMap: make(map[string][]string),
			targetMap:     make(map[string][]string),
		},
		replicatorActions: ConfigMapActions,
	}

	objectStore, objectController := cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				list, err := client.CoreV1().ConfigMaps("").List(lo)
				if err != nil {
					return list, err
				}
				// populate the store already, to avoid believing some items are deleted
				copy := make([]interface{}, len(list.Items))
				for index := range list.Items {
					copy[index] = &list.Items[index]
				}
				repl.objectStore.Replace(copy, "init")
				return list, err
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				return client.CoreV1().ConfigMaps("").Watch(lo)
			},
		},
		&v1.ConfigMap{},
		resyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    repl.ObjectAdded,
			UpdateFunc: func(old interface{}, new interface{}) { repl.ObjectAdded(new) },
			DeleteFunc: repl.ObjectDeleted,
		},
	)

	repl.objectStore = objectStore
	repl.objectController = objectController

	return &repl
}

type configMapActions struct {}

func (*configMapActions) getMeta(object interface{}) *metav1.ObjectMeta {
	return &object.(*v1.ConfigMap).ObjectMeta
}

func (*configMapActions) update(r *replicatorProps, object interface{}, sourceObject interface{}) error {
	sourceConfigMap := sourceObject.(*v1.ConfigMap)
	configMap := object.(*v1.ConfigMap).DeepCopy()

	if sourceConfigMap.Data != nil {
		configMap.Data = make(map[string]string)
		for key, value := range sourceConfigMap.Data {
			configMap.Data[key] = value
		}
	} else {
		configMap.Data = nil
	}

	if sourceConfigMap.BinaryData != nil {
		configMap.BinaryData = make(map[string][]byte)
		for key, value := range sourceConfigMap.BinaryData {
			newValue := make([]byte, len(value))
			copy(newValue, value)
			configMap.BinaryData[key] = newValue
		}
	} else {
		configMap.BinaryData = nil
	}

	log.Printf("updating config map %s/%s", configMap.Namespace, configMap.Name)

	configMap.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	configMap.Annotations[ReplicatedFromVersionAnnotation] = sourceConfigMap.ResourceVersion
	if val, ok := sourceConfigMap.Annotations[ReplicateOnceVersionAnnotation]; ok {
		configMap.Annotations[ReplicateOnceVersionAnnotation] = val
	} else {
		delete(configMap.Annotations, ReplicateOnceVersionAnnotation)
	}

	s, err := r.client.CoreV1().ConfigMaps(configMap.Namespace).Update(configMap)
	if err != nil {
		log.Printf("error while updating config map %s/%s: %s", configMap.Namespace, configMap.Name, err)
		return err
	}

	r.objectStore.Update(s)
	return nil
}

func (*configMapActions) clear(r *replicatorProps, object interface{}) error {
	configMap := object.(*v1.ConfigMap).DeepCopy()
	configMap.Data = nil
	configMap.BinaryData = nil

	log.Printf("clearing config map %s/%s", configMap.Namespace, configMap.Name)

	configMap.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	delete(configMap.Annotations, ReplicatedFromVersionAnnotation)
	delete(configMap.Annotations, ReplicateOnceVersionAnnotation)

	s, err := r.client.CoreV1().ConfigMaps(configMap.Namespace).Update(configMap)
	if err != nil {
		log.Printf("error while clearing config map %s/%s", configMap.Namespace, configMap.Name)
		return err
	}

	r.objectStore.Update(s)
	return nil
}

func (*configMapActions) install(r *replicatorProps, meta *metav1.ObjectMeta, sourceObject interface{}) error {
	sourceConfigMap := sourceObject.(*v1.ConfigMap)
	configMap := v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       sourceConfigMap.Kind,
			APIVersion: sourceConfigMap.APIVersion,
		},
		ObjectMeta: *meta,
	}

	if sourceConfigMap.Data != nil {
		configMap.Data = make(map[string]string)
		for key, value := range sourceConfigMap.Data {
			configMap.Data[key] = value
		}
	}

	if sourceConfigMap.BinaryData != nil {
		configMap.BinaryData = make(map[string][]byte)
		for key, value := range sourceConfigMap.BinaryData {
			newValue := make([]byte, len(value))
			copy(newValue, value)
			configMap.BinaryData[key] = newValue
		}
	}

	log.Printf("installing config map %s/%s", configMap.Namespace, configMap.Name)

	configMap.Annotations = map[string]string{}
	configMap.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	configMap.Annotations[ReplicatedByAnnotation] = fmt.Sprintf("%s/%s",
		sourceConfigMap.Namespace, sourceConfigMap.Name)
	configMap.Annotations[ReplicatedFromVersionAnnotation] = sourceConfigMap.ResourceVersion
	if val, ok := sourceConfigMap.Annotations[ReplicateOnceVersionAnnotation]; ok {
		configMap.Annotations[ReplicateOnceVersionAnnotation] = val
	}

	var s *v1.ConfigMap
	var err error
	if configMap.ResourceVersion == "" {
		s, err = r.client.CoreV1().ConfigMaps(configMap.Namespace).Create(&configMap)
	} else {
		s, err = r.client.CoreV1().ConfigMaps(configMap.Namespace).Update(&configMap)
	}

	if err != nil {
		log.Printf("error while installing config map %s/%s: %s", configMap.Namespace, configMap.Name, err)
		return err
	}

	r.objectStore.Update(s)
	return nil
}

func (*configMapActions) delete(r *replicatorProps, object interface{}) error {
	configMap := object.(*v1.ConfigMap)
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

	r.objectStore.Delete(configMap)
	return nil
}
