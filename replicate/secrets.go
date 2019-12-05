package replicate

import (
	"fmt"
	"log"
	"regexp"
	"time"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

var SecretActions *secretActions = &secretActions{}

// NewSecretReplicator creates a new secret replicator
func NewSecretReplicator(client kubernetes.Interface, resyncPeriod time.Duration, allowAll bool) Replicator {
	repl := objectReplicator{
		replicatorProps: replicatorProps{
			Name:              "secret",
			allowAll:          allowAll,
			client:            client,

			targetsFrom:       make(map[string][]string),
			targetsTo:         make(map[string][]string),

			watchedNamespaces: make(map[string][]string),
			patternNamespaces: make(map[string][]*regexp.Regexp),
		},
		replicatorActions: SecretActions,
	}

	namespaceStore, namespaceController := cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				list, err := client.CoreV1().Namespaces().List(lo)
				if err != nil {
					return list, err
				}
				// populate the store already, to avoid believing some items are deleted
				copy := make([]interface{}, len(list.Items))
				for index := range list.Items {
					copy[index] = &list.Items[index]
				}
				repl.namespaceStore.Replace(copy, "init")
				return list, err
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				return client.CoreV1().Namespaces().Watch(lo)
			},
		},
		&v1.Namespace{},
		resyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    repl.NamespaceAdded,
			UpdateFunc: func(old interface{}, new interface{}) {},
			DeleteFunc: func(obj interface{}) {},
		},
	)

	repl.namespaceStore = namespaceStore
	repl.namespaceController = namespaceController

	objectStore, objectController := cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				list, err := client.CoreV1().Secrets("").List(lo)
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
				return client.CoreV1().Secrets("").Watch(lo)
			},
		},
		&v1.Secret{},
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

type secretActions struct {}

func (*secretActions) getMeta(object interface{}) *metav1.ObjectMeta {
	return &object.(*v1.Secret).ObjectMeta
}

func (*secretActions) update(r *replicatorProps, object interface{}, sourceObject interface{}) error {
	sourceSecret := sourceObject.(*v1.Secret)
	secret := object.(*v1.Secret).DeepCopy()

	if sourceSecret.Data != nil {
		secret.Data = make(map[string][]byte)
		for key, value := range sourceSecret.Data {
			newValue := make([]byte, len(value))
			copy(newValue, value)
			secret.Data[key] = newValue
		}
	} else {
		secret.Data = nil
	}

	log.Printf("updating secret %s/%s", secret.Namespace, secret.Name)

	secret.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	secret.Annotations[ReplicatedFromVersionAnnotation] = sourceSecret.ResourceVersion
	if val, ok := sourceSecret.Annotations[ReplicateOnceVersionAnnotation]; ok {
		secret.Annotations[ReplicateOnceVersionAnnotation] = val
	} else {
		delete(secret.Annotations, ReplicateOnceVersionAnnotation)
	}

	s, err := r.client.CoreV1().Secrets(secret.Namespace).Update(secret)
	if err != nil {
		log.Printf("error while updating secret %s/%s: %s", secret.Namespace, secret.Name, err)
		return err
	}

	r.objectStore.Update(s)
	return nil
}

func (*secretActions) clear(r *replicatorProps, object interface{}) error {
	secret := object.(*v1.Secret).DeepCopy()
	secret.Data = nil

	log.Printf("clearing secret %s/%s", secret.Namespace, secret.Name)

	secret.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	delete(secret.Annotations, ReplicatedFromVersionAnnotation)
	delete(secret.Annotations, ReplicateOnceVersionAnnotation)

	s, err := r.client.CoreV1().Secrets(secret.Namespace).Update(secret)
	if err != nil {
		log.Printf("error while clearing secret %s/%s", secret.Namespace, secret.Name)
		return err
	}

	r.objectStore.Update(s)
	return nil
}

func (*secretActions) install(r *replicatorProps, meta *metav1.ObjectMeta, sourceObject interface{}) error {
	sourceSecret := sourceObject.(*v1.Secret)
	secret := v1.Secret{
		Type: sourceSecret.Type,
		TypeMeta: metav1.TypeMeta{
			Kind:       sourceSecret.Kind,
			APIVersion: sourceSecret.APIVersion,
		},
		ObjectMeta: *meta,
	}

	if sourceSecret.Data != nil {
		secret.Data = make(map[string][]byte)
		for key, value := range sourceSecret.Data {
			newValue := make([]byte, len(value))
			copy(newValue, value)
			secret.Data[key] = newValue
		}
	}

	log.Printf("installing secret %s/%s", secret.Namespace, secret.Name)

	secret.Annotations = map[string]string{}
	secret.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	secret.Annotations[ReplicatedByAnnotation] = fmt.Sprintf("%s/%s",
		sourceSecret.Namespace, sourceSecret.Name)
	secret.Annotations[ReplicatedFromVersionAnnotation] = sourceSecret.ResourceVersion
	if val, ok := sourceSecret.Annotations[ReplicateOnceVersionAnnotation]; ok {
		secret.Annotations[ReplicateOnceVersionAnnotation] = val
	}

	var s *v1.Secret
	var err error
	if secret.ResourceVersion == "" {
		s, err = r.client.CoreV1().Secrets(secret.Namespace).Create(&secret)
	} else {
		s, err = r.client.CoreV1().Secrets(secret.Namespace).Update(&secret)
	}

	if err != nil {
		log.Printf("error while installing secret %s/%s: %s", secret.Namespace, secret.Name, err)
		return err
	}

	r.objectStore.Update(s)
	return nil
}

func (*secretActions) delete(r *replicatorProps, object interface{}) error {
	secret := object.(*v1.Secret)
	log.Printf("deleting secret %s/%s", secret.Namespace, secret.Name)

	options := metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			ResourceVersion: &secret.ResourceVersion,
		},
	}

	err := r.client.CoreV1().Secrets(secret.Namespace).Delete(secret.Name, &options)
	if err != nil {
		log.Printf("error while deleting secret %s/%s: %s", secret.Namespace, secret.Name, err)
		return err
	}

	r.objectStore.Delete(secret)
	return nil
}
