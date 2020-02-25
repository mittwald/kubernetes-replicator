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

type roleReplicator struct {
	replicatorProps
}

// NewRoleReplicator creates a new role replicator
func NewRoleReplicator(client kubernetes.Interface, resyncPeriod time.Duration, allowAll bool) Replicator {
	repl := roleReplicator{
		replicatorProps: replicatorProps{
			allowAll:      allowAll,
			client:        client,
			dependencyMap: make(map[string]map[string]interface{}),
		},
	}

	store, controller := cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				return client.RbacV1().Roles("").List(lo)
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				return client.RbacV1().Roles("").Watch(lo)
			},
		},
		&rbacv1.Role{},
		resyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    repl.RoleAdded,
			UpdateFunc: func(old interface{}, new interface{}) { repl.RoleAdded(new) },
			DeleteFunc: repl.RoleDeleted,
		},
	)

	repl.store = store
	repl.controller = controller

	return &repl
}

func (r *roleReplicator) Synced() bool {
	return r.controller.HasSynced()
}

func (r *roleReplicator) Run() {
	log.Printf("running role controller")
	r.controller.Run(wait.NeverStop)
}

func (r *roleReplicator) RoleAdded(obj interface{}) {
	role := obj.(*rbacv1.Role)
	roleKey := fmt.Sprintf("%s/%s", role.Namespace, role.Name)

	replicas, ok := r.dependencyMap[roleKey]
	if ok {
		log.Printf("role %s has %d dependents", roleKey, len(replicas))
		r.updateDependents(role, replicas)
	}

	val, ok := role.Annotations[ReplicateFromAnnotation]
	if !ok {
		return
	}

	log.Printf("role %s/%s is replicated from %s", role.Namespace, role.Name, val)
	v := strings.SplitN(val, "/", 2)

	if len(v) < 2 {
		return
	}

	if _, ok := r.dependencyMap[val]; !ok {
		r.dependencyMap[val] = make(map[string]interface{})
	}

	r.dependencyMap[val][roleKey] = nil

	sourceObject, exists, err := r.store.GetByKey(val)
	if err != nil {
		log.Printf("could not get role %s: %s", val, err)
		return
	} else if !exists {
		log.Printf("could not get role %s: does not exist", val)
		return
	}

	sourceRole := sourceObject.(*rbacv1.Role)

	r.replicateRole(role, sourceRole)
}

func (r *roleReplicator) replicateRole(role *rbacv1.Role, sourceRole *rbacv1.Role) error {
	// make sure replication is allowed
	if ok, err := r.isReplicationPermitted(&role.ObjectMeta, &sourceRole.ObjectMeta); !ok {
		log.Printf("replication of role %s/%s is not permitted: %s", sourceRole.Namespace, sourceRole.Name, err)
		return err
	}

	targetVersion, ok := role.Annotations[ReplicatedFromVersionAnnotation]
	sourceVersion := sourceRole.ResourceVersion

	if ok && targetVersion == sourceVersion {
		log.Printf("role %s/%s is already up-to-date", role.Namespace, role.Name)
		return nil
	}

	roleCopy := role.DeepCopy()

	roleCopy.Rules = sourceRole.Rules

	log.Printf("updating role %s/%s", role.Namespace, role.Name)

	roleCopy.Annotations[ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	roleCopy.Annotations[ReplicatedFromVersionAnnotation] = sourceRole.ResourceVersion

	s, err := r.client.RbacV1().Roles(role.Namespace).Update(roleCopy)
	if err != nil {
		log.Printf("could not update role %s: %s", role.Name, err)
		return err
	}

	r.store.Update(s)
	return nil
}

func (r *roleReplicator) roleFromStore(key string) (*rbacv1.Role, error) {
	obj, exists, err := r.store.GetByKey(key)
	if err != nil {
		return nil, fmt.Errorf("could not get role %s: %s", key, err)
	}

	if !exists {
		return nil, fmt.Errorf("could not get role %s: does not exist", key)
	}

	role, ok := obj.(*rbacv1.Role)
	if !ok {
		return nil, fmt.Errorf("bad type returned from store: %T", obj)
	}

	return role, nil
}

func (r *roleReplicator) updateDependents(role *rbacv1.Role, dependents map[string]interface{}) error {
	for dependentKey := range dependents {
		log.Printf("updating dependent role %s/%s -> %s", role.Namespace, role.Name, dependentKey)

		targetObject, exists, err := r.store.GetByKey(dependentKey)
		if err != nil {
			log.Printf("could not get dependent role %s: %s", dependentKey, err)
			continue
		} else if !exists {
			log.Printf("could not get dependent role %s: does not exist", dependentKey)
			continue
		}

		targetRole := targetObject.(*rbacv1.Role)

		r.replicateRole(targetRole, role)
	}

	return nil
}

func (r *roleReplicator) RoleDeleted(obj interface{}) {
	role := obj.(*rbacv1.Role)
	roleKey := fmt.Sprintf("%s/%s", role.Namespace, role.Name)

	replicas, ok := r.dependencyMap[roleKey]
	if !ok {
		log.Printf("role %s has no dependents and can be deleted without issues", roleKey)
		return
	}

	for dependentKey := range replicas {
		targetRole, err := r.roleFromStore(dependentKey)
		if err != nil {
			log.Printf("could not load dependent role: %s", err)
			continue
		}

		patch := []JSONPatchOperation{{Operation: "remove", Path: "/rules"}}
		patchBody, err := json.Marshal(&patch)

		if err != nil {
			log.Printf("error while building patch body for role %s: %s", dependentKey, err)
			continue
		}

		log.Printf("clearing dependent role %s", dependentKey)
		log.Printf("patch body: %s", string(patchBody))

		s, err := r.client.RbacV1().Roles(targetRole.Namespace).Patch(targetRole.Name, types.JSONPatchType, patchBody)
		if err != nil {
			log.Printf("error while patching role %s: %s", dependentKey, err)
			continue
		}

		r.store.Update(s)
	}
}
