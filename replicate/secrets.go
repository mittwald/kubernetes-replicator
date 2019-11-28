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

type secretReplicator struct {
	replicatorProps
}

// NewSecretReplicator creates a new secret replicator
func NewSecretReplicator(client kubernetes.Interface, resyncPeriod time.Duration, allowAll bool) Replicator {
	repl := secretReplicator{
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
				list, err := client.CoreV1().Secrets("").List(lo)
				if err != nil {
					return list, err
				}
				// populate the store already, to avoid believing some items are deleted
				copy := make([]interface{}, len(list.Items))
				for index := range list.Items {
					copy[index] = &list.Items[index]
				}
				repl.store.Replace(copy, "init")
				return list, err
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

func (r *secretReplicator) Synced() bool {
	return r.controller.HasSynced()
}

func (r *secretReplicator) Run() {
	log.Printf("running secret controller")
	r.controller.Run(wait.NeverStop)
}

func (r *secretReplicator) SecretAdded(obj interface{}) {

	secret := obj.(*v1.Secret)
	secretKey := fmt.Sprintf("%s/%s", secret.Namespace, secret.Name)

	if val, ok := r.targetMap[secretKey]; ok {
		if annotation, ok := resolveAnnotation(&secret.ObjectMeta, ReplicateToAnnotation); !ok || val != annotation {
			log.Printf("annotation of source secret %s changed", secretKey)

			r.deleteSecret(val, secret)
			delete(r.targetMap, secretKey)
		}
	}

	if replicas, ok := r.dependencyMap[secretKey]; ok {
		log.Printf("secret %s has %d dependents", secretKey, len(replicas))
		r.updateDependents(secret, replicas)
	}

	if val, ok := secret.Annotations[ReplicatedByAnnotation]; ok {
		var sourceSecret *v1.Secret = nil

		sourceObject, exists, err := r.store.GetByKey(val)
		if err != nil {
			log.Printf("could not get secret %s: %s", val, err)
			return

		} else if !exists {
			log.Printf("source secret %s deleted", val)

		} else {
			sourceSecret = sourceObject.(*v1.Secret)

			if !annotationRefersTo(&sourceSecret.ObjectMeta, ReplicateToAnnotation, &secret.ObjectMeta) {
				log.Printf("annotation of source secret %s changed", val)
				sourceSecret = nil
			}
		}

		if sourceSecret == nil {
			r.doDeleteSecret(secret)
			return

		} else {
			r.installSecret("", secret, sourceSecret)
			return
		}
	}

	if val, ok := resolveAnnotation(&secret.ObjectMeta, ReplicateFromAnnotation); ok {
		log.Printf("secret %s is replicated from %s", secretKey, val)

		if _, ok := r.dependencyMap[val]; !ok {
			r.dependencyMap[val] = make([]string, 0, 1)
		}
		r.dependencyMap[val] = append(r.dependencyMap[val], secretKey)

		if sourceObject, exists, err := r.store.GetByKey(val); err != nil {
			log.Printf("could not get secret %s: %s", val, err)
			return

		} else if !exists {
			log.Printf("source secret %s deleted", val)
			r.doClearSecret(secret)
			return

		} else {
			sourceSecret := sourceObject.(*v1.Secret)
			r.replicateSecret(secret, sourceSecret)
			return
		}
	}

	if val, ok := resolveAnnotation(&secret.ObjectMeta, ReplicateToAnnotation); ok {
		log.Printf("secret %s is replicated to %s", secretKey, val)

		r.targetMap[secretKey] = val

		r.installSecret(val, nil, secret)
		return
	}
}

func (r *secretReplicator) replicateSecret(secret *v1.Secret, sourceSecret *v1.Secret) error {
	// make sure replication is allowed
	if ok, err := r.isReplicationPermitted(&secret.ObjectMeta, &sourceSecret.ObjectMeta); !ok {
		log.Printf("replication of secret %s/%s is cancelled: %s", secret.Namespace, secret.Name, err)
		return err
	}

	if ok, err := r.needsUpdate(&secret.ObjectMeta, &sourceSecret.ObjectMeta); !ok {
		log.Printf("replication of secret %s/%s is skipped: %s", secret.Namespace, secret.Name, err)
		return err
	}

	secretCopy := secret.DeepCopy()

	if sourceSecret.Data != nil {
		secretCopy.Data = make(map[string][]byte)
		for key, value := range sourceSecret.Data {
			newValue := make([]byte, len(value))
			copy(newValue, value)
			secretCopy.Data[key] = newValue
		}
	} else {
		secretCopy.Data = nil
	}

	log.Printf("updating secret %s/%s", secret.Namespace, secret.Name)

	secretCopy.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	secretCopy.Annotations[ReplicatedFromVersionAnnotation] = sourceSecret.ResourceVersion

	s, err := r.client.CoreV1().Secrets(secret.Namespace).Update(secretCopy)
	if err != nil {
		log.Printf("error while updating secret %s/%s: %s", secret.Namespace, secret.Name, err)
		return err
	}

	r.store.Update(s)
	return nil
}

func (r *secretReplicator) installSecret(target string, targetSecret *v1.Secret, sourceSecret *v1.Secret) error {
	var targetSplit []string
	if targetSecret == nil {
		targetSplit = strings.SplitN(target, "/", 2)

		if len(targetSplit) != 2 {
			err := fmt.Errorf("illformed annotation %s: expected namespace/name, got %s",
				ReplicatedByAnnotation, target)
			log.Printf("%s", err)
			return err
		}

		if targetObject, exists, err := r.store.GetByKey(target); err != nil {
			log.Printf("could not get secret %s: %s", target, err)
			return err

		} else if exists {
			targetSecret = targetObject.(*v1.Secret)
		}
	} else {
		targetSplit = []string{targetSecret.Namespace, targetSecret.Name}
	}

	if targetSecret != nil {
		if ok, err := r.canReplicateTo(&sourceSecret.ObjectMeta, &targetSecret.ObjectMeta); !ok {
			log.Printf("replication of secret %s/%s is cancelled: %s",
				sourceSecret.Namespace, sourceSecret.Name, err)
			return err
		}

		if ok, err := r.needsUpdate(&targetSecret.ObjectMeta, &sourceSecret.ObjectMeta); !ok {
			log.Printf("replication of secret %s/%s is skipped: %s",
				sourceSecret.Namespace, sourceSecret.Name, err)
			return err
		}
	}

	secretCopy := v1.Secret{
		Type: sourceSecret.Type,
		TypeMeta: metav1.TypeMeta{
			Kind:       sourceSecret.Kind,
			APIVersion: sourceSecret.APIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   targetSplit[0],
			Name:        targetSplit[1],
			Annotations: map[string]string{},
		},
	}

	if sourceSecret.Data != nil {
		secretCopy.Data = make(map[string][]byte)
		for key, value := range sourceSecret.Data {
			newValue := make([]byte, len(value))
			copy(newValue, value)
			secretCopy.Data[key] = newValue
		}
	}

	log.Printf("installing secret %s/%s", secretCopy.Namespace, secretCopy.Name)

	secretCopy.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	secretCopy.Annotations[ReplicatedByAnnotation] = fmt.Sprintf("%s/%s",
		sourceSecret.Namespace, sourceSecret.Name)
	secretCopy.Annotations[ReplicatedFromVersionAnnotation] = sourceSecret.ResourceVersion

	var s *v1.Secret
	var err error
	if targetSecret == nil {
		s, err = r.client.CoreV1().Secrets(secretCopy.Namespace).Create(&secretCopy)
	} else {
		secretCopy.ResourceVersion = targetSecret.ResourceVersion
		s, err = r.client.CoreV1().Secrets(secretCopy.Namespace).Update(&secretCopy)
	}

	if err != nil {
		log.Printf("error while installing secret %s/%s: %s", secretCopy.Namespace, secretCopy.Name, err)
		return err
	}

	r.store.Update(s)
	return nil
}

func (r *secretReplicator) secretFromStore(key string) (*v1.Secret, error) {
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

func (r *secretReplicator) updateDependents(secret *v1.Secret, replicas []string) error {
	secretKey := fmt.Sprintf("%s/%s", secret.Namespace, secret.Name)

	sort.Strings(replicas)
	updatedReplicas := make([]string, 0, 0)
	var previous string

	for _, dependentKey := range replicas {
		// get rid of dupplicates in replicas
		if previous == dependentKey {
			continue
		}
		previous = dependentKey

		targetSecret, err := r.secretFromStore(dependentKey)
		if err != nil {
			log.Printf("could not load dependent secret: %s", err)
			continue
		}

		val, ok := resolveAnnotation(&targetSecret.ObjectMeta, ReplicateFromAnnotation)
		if !ok || val != secretKey {
			log.Printf("annotation of dependent secret %s changed", dependentKey)
			continue
		}

		updatedReplicas = append(updatedReplicas, dependentKey)

		r.replicateSecret(targetSecret, secret)
	}

	if len(updatedReplicas) > 0 {
		r.dependencyMap[secretKey] = updatedReplicas
	} else {
		delete(r.dependencyMap, secretKey)
	}

	return nil
}

func (r *secretReplicator) SecretDeleted(obj interface{}) {
	secret := obj.(*v1.Secret)
	secretKey := fmt.Sprintf("%s/%s", secret.Namespace, secret.Name)

	if val, ok := r.targetMap[secretKey]; ok {
		r.deleteSecret(val, secret)
		delete(r.targetMap, secretKey)
	}

	replicas, ok := r.dependencyMap[secretKey]
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

		if ok, _ := r.clearSecret(dependentKey, secret); ok {
			updatedReplicas = append(updatedReplicas, dependentKey)
		}
	}

	if len(updatedReplicas) > 0 {
		r.dependencyMap[secretKey] = updatedReplicas
	} else {
		delete(r.dependencyMap, secretKey)
	}
}

func (r *secretReplicator) clearSecret(secretKey string, sourceSecret *v1.Secret) (bool, error) {
	targetSecret, err := r.secretFromStore(secretKey)
	if err != nil {
		log.Printf("could not load dependent secret: %s", err)
		return false, err
	}

	if !annotationRefersTo(&targetSecret.ObjectMeta, ReplicateFromAnnotation, &sourceSecret.ObjectMeta) {
		log.Printf("annotation of dependent secret %s changed", secretKey)
		return false, nil
	}

	return true, r.doClearSecret(targetSecret)
}

func (r *secretReplicator) doClearSecret(secret *v1.Secret) error {
	if _, ok := secret.Annotations[ReplicatedFromVersionAnnotation]; !ok {
		log.Printf("secret %s/%s is already up-to-date", secret.Namespace, secret.Name)
		return nil
	}

	secretCopy := secret.DeepCopy()
	secretCopy.Data = nil

	log.Printf("clearing secret %s/%s", secret.Namespace, secret.Name)

	secretCopy.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	delete(secretCopy.Annotations, ReplicatedFromVersionAnnotation)

	s, err := r.client.CoreV1().Secrets(secret.Namespace).Update(secretCopy)
	if err != nil {
		log.Printf("error while clearing secret %s/%s", secret.Namespace, secret.Name)
		return err
	}

	r.store.Update(s)
	return nil
}

func (r *secretReplicator) deleteSecret(secretKey string, sourceSecret *v1.Secret) (bool, error) {
	object, exists, err := r.store.GetByKey(secretKey)

	if err != nil {
		log.Printf("could not get secret %s: %s", secretKey, err)
		return false, err

	} else if !exists {
		log.Printf("could not get secret %s: does not exist", secretKey)
		return false, nil
	}

	secret := object.(*v1.Secret)

	// make sure replication is allowed
	if ok, err := r.canReplicateTo(&sourceSecret.ObjectMeta, &secret.ObjectMeta); !ok {
		log.Printf("deletion of secret %s is cancelled: %s", secretKey, err)
		return false, err
	// delete the secret
	} else {
		return true, r.doDeleteSecret(secret)
	}
}

func (r *secretReplicator) doDeleteSecret(secret *v1.Secret) error {
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

	r.store.Delete(secret)
	return nil
}
