package replicate

import (
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"log"
	"strings"
	"time"
	"fmt"
	"k8s.io/apimachinery/pkg/types"
	"encoding/json"
)

type SecretReplicator struct {
	client     kubernetes.Interface
	store      cache.Store
	controller cache.Controller

	dependencyMap map[string][]string
}

func NewSecretReplicator(client kubernetes.Interface, resyncPeriod time.Duration) *SecretReplicator {
	repl := SecretReplicator{
		client: client,
		dependencyMap: make(map[string][]string),
	}

	store, controller := cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				return client.CoreV1().Secrets("").List(lo)
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				return client.CoreV1().Secrets("").Watch(lo)
			},
		},
		&v1.Secret{},
		resyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    repl.SecretAdded,
			UpdateFunc: func(old interface{}, new interface{}) { repl.SecretAdded(new) },
			DeleteFunc: repl.SecretDeleted,
		},
	)

	repl.store = store
	repl.controller = controller

	return &repl
}

func (r *SecretReplicator) Run() {
	log.Printf("running secret controller")
	r.controller.Run(wait.NeverStop)
}

func (r *SecretReplicator) SecretAdded(obj interface{}) {
	secret := obj.(*v1.Secret)
	secretKey := fmt.Sprintf("%s/%s", secret.Namespace, secret.Name)

	replicas, ok := r.dependencyMap[secretKey]
	if ok {
		log.Printf("secret %s has %d dependents", secretKey, len(replicas))
		r.updateDependents(secret, replicas)
	}

	val, ok := secret.Annotations[ReplicateFromAnnotation]
	if !ok {
		return
	}

	log.Printf("secret %s/%s is replicated from %s", secret.Namespace, secret.Name, val)
	v := strings.SplitN(val, "/", 2)

	if len(v) < 2 {
		return
	}

	sourceObject, exists, err := r.store.GetByKey(val)
	if err != nil {
		log.Printf("could not get secret %s: %s", val, err)
		return
	} else if !exists {
		log.Printf("could not get secret %s: does not exist", val)
		return
	}

	if _, ok := r.dependencyMap[val]; !ok {
		r.dependencyMap[val] = make([]string, 0, 1)
	}

	r.dependencyMap[val] = append(r.dependencyMap[val], secretKey)

	sourceSecret := sourceObject.(*v1.Secret)

	r.replicateSecret(secret, sourceSecret)
}

func (r *SecretReplicator) replicateSecret(secret *v1.Secret, sourceSecret *v1.Secret) error {
	targetVersion, ok := secret.Annotations[ReplicatedFromVersionAnnotation]
	sourceVersion := sourceSecret.ResourceVersion

	if ok && targetVersion == sourceVersion {
		log.Printf("secret %s/%s is already up-to-date", secret.Namespace, secret.Name)
		return nil
	}

	secretCopy := secret.DeepCopy()

	if secretCopy.Data == nil {
		secretCopy.Data = make(map[string][]byte)
	}

	for key, value := range sourceSecret.Data {
		newValue := make([]byte, len(value))
		copy(newValue, value)
		secretCopy.Data[key] = newValue
	}

	log.Printf("updating secret %s/%s", secret.Namespace, secret.Name)

	secretCopy.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	secretCopy.Annotations[ReplicatedFromVersionAnnotation] = sourceSecret.ResourceVersion

	s, err := r.client.CoreV1().Secrets(secret.Namespace).Update(secretCopy)
	if err != nil {
		return err
	}

	r.store.Update(s)
	return nil
}

func (r *SecretReplicator) secretFromStore(key string) (*v1.Secret, error) {
	obj, exists, err := r.store.GetByKey(key)
	if err != nil {
		return nil, fmt.Errorf("could not get secret %s: %s", key, err)
	}

	if !exists {
		return nil, fmt.Errorf("could not get secret %s: does not exist", key)
	}

	secret, ok := obj.(*v1.Secret)
	if !ok {
		return nil, fmt.Errorf("bad type returned from store: %T", obj)
	}

	return secret, nil
}

func (r *SecretReplicator) updateDependents(secret *v1.Secret, dependents []string) error {
	for _, dependentKey := range dependents {
		log.Printf("updating dependent secret %s/%s -> %s", secret.Namespace, secret.Name, dependentKey)

		targetObject, exists, err := r.store.GetByKey(dependentKey)
		if err != nil {
			log.Printf("could not get dependent secret %s: %s", dependentKey, err)
			continue
		} else if !exists {
			log.Printf("could not get dependent secret %s: does not exist", dependentKey)
			continue
		}

		targetSecret := targetObject.(*v1.Secret)

		r.replicateSecret(targetSecret, secret)
	}

	return nil
}

func (r *SecretReplicator) SecretDeleted(obj interface{}) {
	secret := obj.(*v1.Secret)
	secretKey := fmt.Sprintf("%s/%s", secret.Namespace, secret.Name)

	replicas, ok := r.dependencyMap[secretKey]
	if !ok {
		log.Printf("secret %s has no dependents and can be deleted without issues", secretKey)
		return
	}

	for _, dependentKey := range replicas {
		targetSecret, err := r.secretFromStore(dependentKey)
		if err != nil {
			log.Printf("could not load dependent secret: %s", err)
			continue
		}

		patch := []JSONPatchOperation{{Operation: "remove", Path: "/data"},}
		patchBody, err := json.Marshal(&patch)

		if err != nil {
			log.Printf("error while building patch body for secret %s: %s", dependentKey, err)
			continue
		}

		log.Printf("clearing dependent secret %s", dependentKey)
		log.Printf("patch body: %s", string(patchBody))

		s, err := r.client.CoreV1().Secrets(targetSecret.Namespace).Patch(targetSecret.Name, types.JSONPatchType, patchBody)
		if err != nil {
			log.Printf("error while patching secret %s: %s", dependentKey, err)
			continue
		}

		r.store.Update(s)
	}
}
