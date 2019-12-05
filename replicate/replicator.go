package replicate

import (
	"fmt"
	"log"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

type replicatorActions interface {
	getMeta(object interface{}) *metav1.ObjectMeta
	update(r *replicatorProps, object interface{}, sourceObject interface{}) error
	clear(r *replicatorProps, object interface{}) error
	install(r *replicatorProps, meta *metav1.ObjectMeta, sourceObject interface{}) error
	delete(r *replicatorProps, meta interface{}) error
}

type objectReplicator struct {
	replicatorProps
	replicatorActions
}

func (r *objectReplicator) Synced() bool {
	return r.objectController.HasSynced()
}

func (r *objectReplicator) Run() {
	log.Printf("running %s object controller", r.Name)
	r.objectController.Run(wait.NeverStop)
}

func (r *objectReplicator) ObjectAdded(object interface{}) {

	meta := r.getMeta(object)
	key := fmt.Sprintf("%s/%s", meta.Namespace, meta.Name)

	targets, targetPatterns, err := r.getReplicationTargets(meta)
	if err != nil {
		log.Printf("could not parse %s %s: %s", r.Name, key, err)
		return
	}

	if oldTargets, ok := r.targetMap[key]; ok {
		log.Printf("source %s %s changed", r.Name, key)
Targets:
		for _, target := range oldTargets {
			for _, t := range targets {
				if t == target {
					continue Targets
				}
			}
			for _, p := range targetPatterns {
				if p.MatchString(target) {
					continue Targets
				}
			}
			log.Printf("annotation of source %s %s changed: deleting target %s",
				r.Name, key, target)
			r.deleteObject(target, object)
		}
		delete(r.targetMap, key)
	}

	if replicas, ok := r.dependencyMap[key]; ok {
		log.Printf("%s %s has %d dependents", r.Name, key, len(replicas))
		r.updateDependents(object, replicas)
	}

	if val, ok := meta.Annotations[ReplicatedByAnnotation]; ok {
		log.Printf("%s %s is replicated by %s", r.Name, key, val)
		sourceObject, exists, err := r.objectStore.GetByKey(val)
		sourceMeta := r.getMeta(sourceObject)

		if err != nil {
			log.Printf("could not get %s %s: %s", r.Name, val, err)
			return

		} else if !exists {
			log.Printf("source %s %s deleted: deleting target %s", r.Name, val, key)
			sourceMeta = nil

		} else if ok, err := r.isReplicatedTo(sourceMeta, meta); err != nil {
			log.Printf("could not parse %s %s: %s", r.Name, val, err)
			return

		} else if !ok {
			log.Printf("source %s %s is not replicated to %s: deleting target", r.Name, val, key)
			sourceMeta = nil
		}

		if sourceMeta == nil {
			r.doDeleteObject(object)
			return

		} else {
			r.installObject("", object, sourceObject)
			return
		}
	}

	if val, ok := resolveAnnotation(meta, ReplicateFromAnnotation); ok {
		log.Printf("%s %s is replicated from %s", r.Name, key, val)

		if _, ok := r.dependencyMap[val]; !ok {
			r.dependencyMap[val] = make([]string, 0, 1)
		}
		r.dependencyMap[val] = append(r.dependencyMap[val], key)

		if sourceObject, exists, err := r.objectStore.GetByKey(val); err != nil {
			log.Printf("could not get %s %s: %s", r.Name, val, err)
			return

		} else if !exists {
			log.Printf("source %s %s deleted: deleting target %s", r.Name, val, key)
			r.doClearObject(object)
			return

		} else {
			r.replicateObject(object, sourceObject)
			return
		}
	}

	if len(targets) > 0 || len(targetPatterns) > 0 {

		if len(targetPatterns) > 0 {
			namespaces := r.namespaceStore.ListKeys()
			seen := map[string]bool{}
			for _, t := range(targets) {
				seen[t] = true
			}

			for _, p := range targetPatterns {
				for _, t := range p.Targets(namespaces) {
					if !seen[t] {
						seen[t] = true
						targets = append(targets, t)
					}
				}
			}
		}

		r.targetMap[key] = targets
		for _, t := range(targets) {
			log.Printf("%s %s is replicated to %s", r.Name, key, t)
			r.installObject(t, nil, object)
		}
		return
	}
}

func (r *objectReplicator) replicateObject(object interface{}, sourceObject  interface{}) error {
	meta := r.getMeta(object)
	sourceMeta := r.getMeta(sourceObject)
	// make sure replication is allowed
	if ok, err := r.isReplicationPermitted(meta, sourceMeta); !ok {
		log.Printf("replication of %s %s/%s is cancelled: %s", r.Name, meta.Namespace, meta.Name, err)
		return err
	}

	if ok, err := r.needsUpdate(meta, sourceMeta); !ok {
		log.Printf("replication of %s %s/%s is skipped: %s", r.Name, meta.Namespace, meta.Name, err)
		return err
	}

	return r.update(&r.replicatorProps, object, sourceObject)
}

func (r *objectReplicator) installObject(target string, targetObject interface{}, sourceObject interface{}) error {
	var targetMeta *metav1.ObjectMeta
	sourceMeta := r.getMeta(sourceObject)
	var targetSplit []string

	if targetObject == nil {
		targetSplit = strings.SplitN(target, "/", 2)

		if len(targetSplit) != 2 {
			err := fmt.Errorf("illformed annotation %s in %s %s/%s: expected namespace/name, got %s",
				ReplicatedByAnnotation, r.Name, sourceMeta.Namespace, sourceMeta.Name, target)
			log.Printf("%s", err)
			return err
		}

		if targetObject, exists, err := r.objectStore.GetByKey(target); err != nil {
			log.Printf("could not get %s %s: %s", r.Name, target, err)
			return err

		} else if exists {
			targetMeta = r.getMeta(targetObject)
		}
	} else {
		targetMeta = r.getMeta(targetObject)
		targetSplit = []string{targetMeta.Namespace, targetMeta.Name}

		if ok, err := r.isReplicatedBy(targetMeta, sourceMeta); !ok {
			log.Printf("replication of %s %s/%s is cancelled: %s",
				r.Name, sourceMeta.Namespace, sourceMeta.Name, err)
			return err
		}
	}

	if targetMeta != nil {
		if ok, err := r.needsUpdate(targetMeta, sourceMeta); !ok {
			log.Printf("replication of %s %s/%s is skipped: %s",
				r.Name, sourceMeta.Namespace, sourceMeta.Name, err)
			return err
		}
	}

	copyMeta := metav1.ObjectMeta{
		Namespace:   targetSplit[0],
		Name:        targetSplit[1],
		Annotations: map[string]string{},
	}

	if targetMeta != nil {
		copyMeta.ResourceVersion = targetMeta.ResourceVersion
	}

	return r.install(&r.replicatorProps, &copyMeta, sourceObject)
}

func (r *objectReplicator) objectFromStore(key string) (interface{}, *metav1.ObjectMeta, error) {
	if object, exists, err := r.objectStore.GetByKey(key); err != nil {
		return nil, nil, fmt.Errorf("could not get %s %s: %s", r.Name, key, err)
	} else if !exists {
		return nil, nil, fmt.Errorf("could not get %s %s: does not exist", r.Name, key)
	} else {
		return object, r.getMeta(object), nil
	}
}

func (r *objectReplicator) updateDependents(object interface{}, replicas []string) error {
	meta := r.getMeta(object)
	key := fmt.Sprintf("%s/%s", meta.Namespace, meta.Name)

	sort.Strings(replicas)
	updatedReplicas := make([]string, 0, 0)
	var previous string

	for _, dependentKey := range replicas {
		// get rid of dupplicates in replicas
		if previous == dependentKey {
			continue
		}
		previous = dependentKey

		targetObject, targetMeta, err := r.objectFromStore(dependentKey)
		if err != nil {
			log.Printf("could not load dependent %s: %s", r.Name, err)
			continue
		}

		val, ok := resolveAnnotation(targetMeta, ReplicateFromAnnotation)
		if !ok || val != key {
			log.Printf("annotation of dependent %s %s changed", r.Name, dependentKey)
			continue
		}

		updatedReplicas = append(updatedReplicas, dependentKey)

		r.replicateObject(targetObject, object)
	}

	if len(updatedReplicas) > 0 {
		r.dependencyMap[key] = updatedReplicas
	} else {
		delete(r.dependencyMap, key)
	}

	return nil
}

func (r *objectReplicator) ObjectDeleted(object interface{}) {
	meta := r.getMeta(object)
	key := fmt.Sprintf("%s/%s", meta.Namespace, meta.Name)

	if targets, ok := r.targetMap[key]; ok {
		for _, t := range targets {
			r.deleteObject(t, object)
		}
		delete(r.targetMap, key)
	}

	replicas, ok := r.dependencyMap[key]
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

		if ok, _ := r.clearObject(dependentKey, object); ok {
			updatedReplicas = append(updatedReplicas, dependentKey)
		}
	}

	if len(updatedReplicas) > 0 {
		r.dependencyMap[key] = updatedReplicas
	} else {
		delete(r.dependencyMap, key)
	}
}

func (r *objectReplicator) clearObject(key string, sourceObject interface{}) (bool, error) {
	sourceMeta := r.getMeta(sourceObject)

	targetObject, targetMeta, err := r.objectFromStore(key)
	if err != nil {
		log.Printf("could not load dependent %s: %s", r.Name, err)
		return false, err
	}

	if !annotationRefersTo(targetMeta, ReplicateFromAnnotation, sourceMeta) {
		log.Printf("annotation of dependent %s %s changed", r.Name, key)
		return false, nil
	}

	return true, r.doClearObject(targetObject)
}

func (r *objectReplicator) doClearObject(object interface{}) error {
	meta := r.getMeta(object)

	if _, ok := meta.Annotations[ReplicatedFromVersionAnnotation]; !ok {
		log.Printf("%s %s/%s is already up-to-date", r.Name, meta.Namespace, meta.Name)
		return nil
	}

	return r.clear(&r.replicatorProps, object)
}

func (r *objectReplicator) deleteObject(key string, sourceObject interface{}) (bool, error) {
	sourceMeta := r.getMeta(sourceObject)

	object, meta, err := r.objectFromStore(key)
	if err != nil {
		log.Printf("could not get %s %s: %s", r.Name, key, err)
		return false, err
	}

	// make sure replication is allowed
	if ok, err := r.isReplicatedBy(meta, sourceMeta); !ok {
		log.Printf("deletion of %s %s is cancelled: %s", r.Name, key, err)
		return false, err
	// delete the object
	} else {
		return true, r.doDeleteObject(object)
	}
}

func (r *objectReplicator) doDeleteObject(object interface{}) error {
	return r.delete(&r.replicatorProps, object)
}
