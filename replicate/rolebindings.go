package replicate

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type roleBindingReplicator struct {
	replicatorProps
}

// NewRoleBindingReplicator creates a new roleBinding replicator
func NewRoleBindingReplicator(client kubernetes.Interface, resyncPeriod time.Duration, allowAll bool) Replicator {
	repl := roleBindingReplicator{
		replicatorProps: replicatorProps{
			allowAll:      allowAll,
			client:        client,
			dependencyMap: make(map[string]map[string]interface{}),
		},
	}

	store, controller := cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				return client.RbacV1().RoleBindings("").List(lo)
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				return client.RbacV1().RoleBindings("").Watch(lo)
			},
		},
		&rbacv1.RoleBinding{},
		resyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    repl.RoleBindingAdded,
			UpdateFunc: func(old interface{}, new interface{}) { repl.RoleBindingAdded(new) },
			DeleteFunc: repl.RoleBindingDeleted,
		},
	)

	repl.store = store
	repl.controller = controller

	return &repl
}

func (r *roleBindingReplicator) Synced() bool {
	return r.controller.HasSynced()
}

func (r *roleBindingReplicator) Run() {
	log.Printf("running roleBinding controller")
	r.controller.Run(wait.NeverStop)
}

func (r *roleBindingReplicator) RoleBindingAdded(obj interface{}) {
	roleBinding := obj.(*rbacv1.RoleBinding)
	roleBindingKey := fmt.Sprintf("%s/%s", roleBinding.Namespace, roleBinding.Name)

	replicas, ok := r.dependencyMap[roleBindingKey]
	if ok {
		log.Printf("roleBinding %s has %d dependents", roleBindingKey, len(replicas))
		r.updateDependents(roleBinding, replicas)
	}

	val, ok := roleBinding.Annotations[ReplicateFromAnnotation]
	if !ok {
		return
	}

	log.Printf("roleBinding %s/%s is replicated from %s", roleBinding.Namespace, roleBinding.Name, val)
	v := strings.SplitN(val, "/", 2)

	if len(v) < 2 {
		return
	}

	if _, ok := r.dependencyMap[val]; !ok {
		r.dependencyMap[val] = make(map[string]interface{})
	}

	r.dependencyMap[val][roleBindingKey] = nil

	sourceObject, exists, err := r.store.GetByKey(val)
	if err != nil {
		log.Printf("could not get roleBinding %s: %s", val, err)
		return
	} else if !exists {
		log.Printf("could not get roleBinding %s: does not exist", val)
		return
	}

	sourceRole := sourceObject.(*rbacv1.RoleBinding)

	r.replicateRoleBinding(roleBinding, sourceRole)
}

func (r *roleBindingReplicator) replicateRoleBinding(roleBinding *rbacv1.RoleBinding, sourceRole *rbacv1.RoleBinding) error {
	// make sure replication is allowed
	if ok, err := r.isReplicationPermitted(&roleBinding.ObjectMeta, &sourceRole.ObjectMeta); !ok {
		log.Printf("replication of roleBinding %s/%s is not permitted: %s", sourceRole.Namespace, sourceRole.Name, err)
		return err
	}

	targetVersion, ok := roleBinding.Annotations[ReplicatedFromVersionAnnotation]
	sourceVersion := sourceRole.ResourceVersion

	if ok && targetVersion == sourceVersion {
		log.Printf("roleBinding %s/%s is already up-to-date", roleBinding.Namespace, roleBinding.Name)
		return nil
	}

	roleBindingCopy := roleBinding.DeepCopy()
	roleBindingCopy.Subjects = sourceRole.Subjects

	log.Printf("updating roleBinding %s/%s", roleBinding.Namespace, roleBinding.Name)

	roleBindingCopy.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	roleBindingCopy.Annotations[ReplicatedFromVersionAnnotation] = sourceRole.ResourceVersion

	s, err := r.client.RbacV1().RoleBindings(roleBinding.Namespace).Update(roleBindingCopy)
	if err != nil {
		log.Printf("could not update roleBinding %s: %s", roleBinding.Name, err)
		return err
	}

	r.store.Update(s)
	return nil
}

func (r *roleBindingReplicator) roleBindingFromStore(key string) (*rbacv1.RoleBinding, error) {
	obj, exists, err := r.store.GetByKey(key)
	if err != nil {
		return nil, fmt.Errorf("could not get roleBinding %s: %s", key, err)
	}

	if !exists {
		return nil, fmt.Errorf("could not get roleBinding %s: does not exist", key)
	}

	roleBinding, ok := obj.(*rbacv1.RoleBinding)
	if !ok {
		return nil, fmt.Errorf("bad type returned from store: %T", obj)
	}

	return roleBinding, nil
}

func (r *roleBindingReplicator) updateDependents(roleBinding *rbacv1.RoleBinding, dependents map[string]interface{}) error {
	for dependentKey := range dependents {
		log.Printf("updating dependent roleBinding %s/%s -> %s", roleBinding.Namespace, roleBinding.Name, dependentKey)

		targetObject, exists, err := r.store.GetByKey(dependentKey)
		if err != nil {
			log.Printf("could not get dependent roleBinding %s: %s", dependentKey, err)
			continue
		} else if !exists {
			log.Printf("could not get dependent roleBinding %s: does not exist", dependentKey)
			continue
		}

		targetRole := targetObject.(*rbacv1.RoleBinding)

		r.replicateRoleBinding(targetRole, roleBinding)
	}

	return nil
}

func (r *roleBindingReplicator) RoleBindingDeleted(obj interface{}) {
	roleBinding := obj.(*rbacv1.RoleBinding)
	roleBindingKey := fmt.Sprintf("%s/%s", roleBinding.Namespace, roleBinding.Name)

	replicas, ok := r.dependencyMap[roleBindingKey]
	if !ok {
		log.Printf("roleBinding %s has no dependents and can be deleted without issues", roleBindingKey)
		return
	}

	for dependentKey := range replicas {
		targetRole, err := r.roleBindingFromStore(dependentKey)
		if err != nil {
			log.Printf("could not load dependent roleBinding: %s", err)
			continue
		}

		patch := []JSONPatchOperation{{Operation: "remove", Path: "/subjects"}}
		patchBody, err := json.Marshal(&patch)

		if err != nil {
			log.Printf("error while building patch body for roleBinding %s: %s", dependentKey, err)
			continue
		}

		log.Printf("clearing dependent roleBinding %s", dependentKey)
		log.Printf("patch body: %s", string(patchBody))

		s, err := r.client.RbacV1().RoleBindings(targetRole.Namespace).Patch(targetRole.Name, types.JSONPatchType, patchBody)
		if err != nil {
			log.Printf("error while patching roleBinding %s: %s", dependentKey, err)
			continue
		}

		r.store.Update(s)
	}
}
