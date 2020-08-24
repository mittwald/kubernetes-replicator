package common

import (
	"fmt"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ReplicatorConfig struct {
	Kind         string
	Client       kubernetes.Interface
	ResyncPeriod time.Duration
	AllowAll     bool
	ListFunc     cache.ListFunc
	WatchFunc    cache.WatchFunc
	ObjType      runtime.Object
}

type UpdateFuncs struct {
	ReplicateDataFrom        func(source interface{}, target interface{}) error
	ReplicateObjectTo        func(source interface{}, target *v1.Namespace) error
	PatchDeleteDependent     func(sourceKey string, target interface{}) (interface{}, error)
	DeleteReplicatedResource func(target interface{}) error
}

type GenericReplicator struct {
	ReplicatorConfig
	Store      cache.Store
	Controller cache.Controller

	NamespaceStore      cache.Store
	NamespaceController cache.Controller

	DependencyMap map[string]map[string]interface{}
	UpdateFuncs   UpdateFuncs
}

// NewReplicator creates a new generic replicator
func NewGenericReplicator(config ReplicatorConfig) *GenericReplicator {
	repl := GenericReplicator{
		ReplicatorConfig: config,
		DependencyMap:    make(map[string]map[string]interface{}),
	}

	store, controller := cache.NewInformer(
		&cache.ListWatch{
			ListFunc:  config.ListFunc,
			WatchFunc: config.WatchFunc,
		},
		config.ObjType,
		config.ResyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    repl.ResourceAdded,
			UpdateFunc: func(old interface{}, new interface{}) { repl.ResourceAdded(new) },
			DeleteFunc: repl.ResourceDeleted,
		},
	)

	repl.NamespaceStore, repl.NamespaceController = cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				return config.Client.CoreV1().Namespaces().List(lo)
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				return config.Client.CoreV1().Namespaces().Watch(lo)
			},
		},
		&v1.Namespace{},
		config.ResyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				repl.NamespaceAdded(obj.(*v1.Namespace))
			},
		},
	)

	repl.Store = store
	repl.Controller = controller

	return &repl
}

// IsReplicationPermitted checks if replication is allowed in annotations of the source object
// Returns true if replication is allowed. If replication is not allowed returns false with
// error message
func (r *GenericReplicator) IsReplicationPermitted(object *metav1.ObjectMeta, sourceObject *metav1.ObjectMeta) (bool, error) {
	if r.AllowAll {
		return true, nil
	}

	// make sure source object allows replication
	annotationAllowed, ok := sourceObject.Annotations[ReplicationAllowed]
	if !ok {
		return false, fmt.Errorf("source %s/%s does not allow replication. %s will not be replicated",
			sourceObject.Namespace, sourceObject.Name, object.Name)
	}
	annotationAllowedBool, err := strconv.ParseBool(annotationAllowed)

	// check if source object allows replication
	if err != nil || !annotationAllowedBool {
		return false, fmt.Errorf("source %s/%s does not allow replication. %s will not be replicated",
			sourceObject.Namespace, sourceObject.Name, object.Name)
	}

	// check if the target namespace is permitted
	annotationAllowedNamespaces, ok := sourceObject.Annotations[ReplicationAllowedNamespaces]
	if !ok {
		return false, fmt.Errorf(
			"source %s/%s does not allow replication (%s annotation missing). %s will not be replicated",
			sourceObject.Namespace, sourceObject.Name, ReplicationAllowedNamespaces, object.Name)
	}
	allowedNamespaces := strings.Split(annotationAllowedNamespaces, ",")
	allowed := false
	for _, ns := range allowedNamespaces {
		ns := strings.TrimSpace(ns)

		if matched, _ := regexp.MatchString(ns, object.Namespace); matched {
			log.Tracef("Namespace '%s' matches '%s' -- allowing replication", object.Namespace, ns)
			allowed = true
			break
		}
	}

	err = nil
	if !allowed {
		err = fmt.Errorf(
			"source %s/%s does not allow replication in namespace %s. %s will not be replicated",
			sourceObject.Namespace, sourceObject.Name, object.Namespace, object.Name)
	}
	return allowed, err
}

func (r *GenericReplicator) Synced() bool {
	return r.Controller.HasSynced()
}

func (r *GenericReplicator) Run() {
	log.WithField("kind", r.Kind).Infof("running %s controller", r.Kind)
	go r.NamespaceController.Run(wait.NeverStop)
	r.Controller.Run(wait.NeverStop)
}

// NamespaceAdded replicates resources with ReplicateTo annotation into newly created namespaces
func (r *GenericReplicator) NamespaceAdded(ns *v1.Namespace) {
	logger := log.WithField("kind", r.Kind).WithField("target", ns.Name)
	for _, obj := range r.Store.List() {
		objectMeta := MustGetObjectMeta(obj)
		replicatedList := make([]string, 0)
		namespaceList, found := objectMeta.Annotations[ReplicateTo]
		if found {
			if err := r.resourceAddedReplicateTo(obj, namespaceList, ns); err != nil {
				logger.
					WithError(err).
					Errorf("Failed replicating the resource to new namespace %s: %v", ns.Name, err)
			} else {
				replicatedList = append(replicatedList, ns.Name)
			}
			key := MustGetKey(objectMeta)
			logger.WithField("source", key).Infof("Replicated %s to: %v", key, replicatedList)
		}
	}
}

// ResourceAdded checks resources with ReplicateTo or ReplicateFromAnnotation annotation
func (r *GenericReplicator) ResourceAdded(obj interface{}) {
	objectMeta := MustGetObjectMeta(obj)
	cacheKey := MustGetKey(objectMeta)
	logger := log.WithField("kind", r.Kind).WithField("resource", cacheKey)

	replicas, ok := r.DependencyMap[cacheKey]
	if ok {
		logger.Debugf("objectMeta %s has %d dependents", cacheKey, len(replicas))
		if err := r.updateDependents(obj, replicas); err != nil {
			logger.WithError(err).
				Errorf("Failed to update cache for %s: %v", MustGetKey(objectMeta), err)
		}
	}

	// Match resources with "replicate-from" annotation
	source, replicateFrom := objectMeta.Annotations[ReplicateFromAnnotation]
	if replicateFrom {
		if err := r.resourceAddedReplicateFrom(source, obj); err != nil {
			logger.WithError(err).Errorf(
				"Could not copy %s -> %s: %v",
				source, MustGetKey(objectMeta), err,
			)
		}
		return
	}

	// Match resources with "replicate-to" annotation
	namespaceList, replicateTo := objectMeta.Annotations[ReplicateTo]
	if replicateTo {
		if err := r.resourceAddedReplicateTo(obj, namespaceList, nil); err != nil {
			logger.
				WithError(err).
				Errorf(
					"Could not replicate %s to other namespaces: %+v",
					MustGetKey(objectMeta), err,
				)
		}
		return
	}
}

// resourceAddedReplicateFrom replicates resources with ReplicateFromAnnotation
func (r *GenericReplicator) resourceAddedReplicateFrom(sourceLocation string, target interface{}) error {
	cacheKey := MustGetKey(target)

	logger := log.WithField("kind", r.Kind).WithField("source", sourceLocation).WithField("target", cacheKey)
	logger.Debugf("%s %s is replicated from %s", r.Kind, cacheKey, sourceLocation)
	v := strings.SplitN(sourceLocation, "/", 2)

	if len(v) < 2 {
		return errors.Errorf("Invalid source location expected '<namespace>/<name>', got '%s'", sourceLocation)
	}

	if _, ok := r.DependencyMap[sourceLocation]; !ok {
		r.DependencyMap[sourceLocation] = make(map[string]interface{})
	}

	r.DependencyMap[sourceLocation][cacheKey] = nil

	sourceObject, exists, err := r.Store.GetByKey(sourceLocation)
	if err != nil {
		return errors.Wrapf(err, "Could not get source %s: %v", sourceLocation, err)
	} else if !exists {
		return errors.Errorf("Could not get source %s: does not exist", sourceLocation)
	}

	if err := r.UpdateFuncs.ReplicateDataFrom(sourceObject, target); err != nil {
		return errors.Wrapf(err, "Failed to replicate %s target %s -> %s: %v",
			r.Kind, MustGetKey(sourceObject), cacheKey, err,
		)
	}

	return nil
}

// resourceAddedReplicateFrom replicates resources with ReplicateTo
func (r *GenericReplicator) resourceAddedReplicateTo(obj interface{}, namespaceList string, targetNamespace *v1.Namespace) error {
	cacheKey := MustGetKey(obj)
	logger := log.WithField("kind", r.Kind).WithField("source", cacheKey)

	logger.Infof("%s %s to be replicated to: [%s]", r.Kind, cacheKey, namespaceList)
	filters := strings.Split(namespaceList, ",")

	var namespaces []v1.Namespace
	if targetNamespace == nil {
		list, err := r.Client.CoreV1().Namespaces().List(metav1.ListOptions{})
		if err != nil {
			return errors.Wrapf(err, "Failed to list namespaces: %v", err)
		}
		namespaces = list.Items
	} else {
		namespaces = make([]v1.Namespace, 1)
		namespaces[0] = *targetNamespace
	}

	replicatedTo := make([]string, 0)
	for _, namespace := range namespaces {
		for _, ns := range filters {
			ns = strings.TrimSpace(ns)
			if matched, _ := regexp.MatchString(ns, namespace.Name); matched {
				if namespace.Name == MustGetObjectMeta(obj).Namespace {
					// Don't replicate upon itself
					continue
				}

				if err := r.UpdateFuncs.ReplicateObjectTo(obj, &namespace); err != nil {
					return errors.Wrapf(err, "Failed to replicate %s %s -> %s: %v",
						r.Kind, cacheKey, namespace.Name, err,
					)
				} else {
					replicatedTo = append(replicatedTo, namespace.Name)
				}
				break

			}
		}
	}

	logger.Debugf("Replicated %s to %d namespaces: %v", cacheKey, len(replicatedTo), replicatedTo)
	return nil
}

func (r *GenericReplicator) updateDependents(obj interface{}, dependents map[string]interface{}) error {
	cacheKey := MustGetKey(obj)
	logger := log.WithField("kind", r.Kind).WithField("source", cacheKey)

	for dependentKey := range dependents {
		logger.Infof("updating dependent %s %s -> %s", r.Kind, cacheKey, dependentKey)

		targetObject, exists, err := r.Store.GetByKey(dependentKey)
		if err != nil {
			logger.Debugf("could not get dependent %s %s: %s", r.Kind, dependentKey, err)
			continue
		} else if !exists {
			logger.Debugf("could not get dependent %s %s: does not exist", r.Kind, dependentKey)
			continue
		}

		if err := r.UpdateFuncs.ReplicateDataFrom(obj, targetObject); err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

// ObjectFromStore gets object from store cache
func (r *GenericReplicator) ObjectFromStore(key string) (interface{}, error) {
	obj, exists, err := r.Store.GetByKey(key)
	if err != nil {
		return nil, errors.Errorf("could not get %s %s: %s", r.Kind, key, err)
	}

	if !exists {
		return nil, errors.Errorf("could not get %s %s: does not exist", r.Kind, key)
	}

	return obj, nil
}

// ResourceDeleted watches for the deletion of resources
func (r *GenericReplicator) ResourceDeleted(source interface{}) {
	sourceKey := MustGetKey(source)
	logger := log.WithField("kind", r.Kind).WithField("source", sourceKey)
	logger.Debugf("Deleting %s %s", r.Kind, sourceKey)

	r.ResourceDeletedReplicateTo(source)
	r.ResourceDeletedReplicateFrom(source)
}

func (r *GenericReplicator) ResourceDeletedReplicateTo(source interface{}) {
	sourceKey := MustGetKey(source)
	logger := log.WithField("kind", r.Kind).WithField("source", sourceKey)
	objMeta := MustGetObjectMeta(source)
	namespaceList, replicateTo := objMeta.Annotations[ReplicateTo]
	if replicateTo {
		filters := strings.Split(namespaceList, ",")
		list, err := r.Client.CoreV1().Namespaces().List(metav1.ListOptions{})
		if err != nil {
			err = errors.Wrapf(err, "Failed to list namespaces: %v", err)
			logger.WithError(err).Errorf("Could not get namespaces: %+v", err)
		} else {
			r.DeleteResources(source, list, filters)
		}
	}
}

func (r *GenericReplicator) DeleteResources(source interface{}, list *v1.NamespaceList, filters []string) {
	for _, namespace := range list.Items {
		for _, ns := range filters {
			ns = strings.TrimSpace(ns)
			if matched, _ := regexp.MatchString(ns, namespace.Name); matched {
				r.DeleteResource(namespace, source)
			}
		}
	}
}

func (r *GenericReplicator) DeleteResource(namespace v1.Namespace, source interface{}) {
	sourceKey := MustGetKey(source)
	logger := log.WithField("kind", r.Kind).WithField("source", sourceKey)
	objMeta := MustGetObjectMeta(source)

	if namespace.Name == objMeta.Namespace {
		// Don't work upon itself
		return
	}
	targetLocation := fmt.Sprintf("%s/%s", namespace.Name, objMeta.Name)
	targetResource, exists, err := r.Store.GetByKey(targetLocation)
	if err != nil {
		logger.WithError(err).Errorf("Could not get objectMeta %s: %+v", targetLocation, err)
		return
	}
	if !exists {
		return
	}
	if err := r.UpdateFuncs.DeleteReplicatedResource(targetResource); err != nil {
		logger.WithError(err).Errorf("Could not delete resource %s: %+v", targetLocation, err)
	}
}

func (r *GenericReplicator) ResourceDeletedReplicateFrom(source interface{}) {
	sourceKey := MustGetKey(source)
	logger := log.WithField("kind", r.Kind).WithField("source", sourceKey)
	replicas, ok := r.DependencyMap[sourceKey]
	if !ok {
		logger.Debugf("%s %s has no dependents and can be deleted without issues", r.Kind, sourceKey)
		return
	}

	for dependentKey := range replicas {
		target, err := r.ObjectFromStore(dependentKey)
		if err != nil {
			logger.WithError(err).Warnf("could not load dependent %s %s: %v", r.Kind, dependentKey, err)
			continue
		}
		s, err := r.UpdateFuncs.PatchDeleteDependent(MustGetKey(source), target)
		if err != nil {
			logger.WithError(err).Warnf("could not patch dependent %s %s: %v", r.Kind, dependentKey, err)
			continue
		}
		if err := r.Store.Update(s); err != nil {
			logger.WithError(err).Errorf("Error updating store for %s %s: %v", r.Kind, MustGetKey(s), err)
		}
	}
}

