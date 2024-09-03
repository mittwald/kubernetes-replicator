package common

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/labels"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
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

	DependencyMap map[string]map[string]interface{}
	UpdateFuncs   UpdateFuncs

	// ReplicateToList is a set that caches the names of all secrets that have a
	// "replicate-to" annotation.
	ReplicateToList GenericMap[string, struct{}]

	// ReplicateToMatchingList is a set that caches the names of all secrets
	// that have a "replicate-to-matching" annotation.
	ReplicateToMatchingList GenericMap[string, labels.Selector]
}

// NewGenericReplicator creates a new generic replicator
func NewGenericReplicator(config ReplicatorConfig) *GenericReplicator {
	repl := GenericReplicator{
		ReplicatorConfig:        config,
		DependencyMap:           make(map[string]map[string]interface{}),
		ReplicateToList:         GenericMap[string, struct{}]{},
		ReplicateToMatchingList: GenericMap[string, labels.Selector]{},
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

	namespaceWatcher.OnNamespaceAdded(config.Client, config.ResyncPeriod, repl.NamespaceAdded)
	namespaceWatcher.OnNamespaceUpdated(config.Client, config.ResyncPeriod, repl.NamespaceUpdated)

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
		ns := BuildStrictRegex(ns)

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
	r.Controller.Run(wait.NeverStop)
}

// NamespaceAdded replicates resources with ReplicateTo and ReplicateToMatching
// annotations into newly created namespaces.
func (r *GenericReplicator) NamespaceAdded(ns *v1.Namespace) {
	logger := log.WithField("kind", r.Kind).WithField("target", ns.Name)
	r.ReplicateToList.Range(func(sourceKey string, _ struct{}) bool {
		logger := logger.WithField("resource", sourceKey)
		obj, exists, err := r.Store.GetByKey(sourceKey)

		if err != nil {
			log.WithError(err).Error("error fetching object from store")
			return true
		} else if !exists {
			log.Warn("object not found in store")
			return true
		}

		objectMeta := MustGetObject(obj)
		replicatedList := make([]string, 0)
		namespacePatterns, found := objectMeta.GetAnnotations()[ReplicateTo]
		if found {
			if err := r.replicateResourceToMatchingNamespaces(obj, namespacePatterns, []v1.Namespace{*ns}); err != nil {
				logger.
					WithError(err).
					Errorf("Failed replicating the resource to the new namespace %s: %v", ns.Name, err)
			} else {
				replicatedList = append(replicatedList, ns.Name)
			}

		}

		return true
	})

	namespaceLabels := labels.Set(ns.Labels)
	r.ReplicateToMatchingList.Range(func(sourceKey string, selector labels.Selector) bool {
		logger := logger.WithField("resource", sourceKey)

		obj, exists, err := r.Store.GetByKey(sourceKey)
		if err != nil {
			log.WithError(err).Error("error fetching object from store")
			return true
		} else if !exists {
			log.Warn("object not found in store")
			return true
		}

		if !selector.Matches(namespaceLabels) {
			return true
		}

		if _, err := r.replicateResourceToNamespaces(obj, []v1.Namespace{*ns}); err != nil {
			logger.WithError(err).Error("error while replicating object to namespace")
		}
		return true
	})
}

// NamespaceUpdated checks if namespace's labels changed and deletes any 'replicate-to-matching' resources
// the namespace no longer qualifies for. Then it attempts to replicate resources into the updated ns based
// on the updated set of labels
func (r *GenericReplicator) NamespaceUpdated(nsOld *v1.Namespace, nsNew *v1.Namespace) {
	logger := log.WithField("kind", r.Kind).WithField("target", nsNew.Name)
	// check if labels changed
	if reflect.DeepEqual(nsNew.Labels, nsOld.Labels) {
		logger.Debug("labels didn't change")
		return
	} else {
		logger.Infof("labels of namespace %s changed, attempting to delete %ss that no longer match", nsNew.Name, r.Kind)
		// delete any resources where namespace labels no longer match
		var newLabelSet labels.Set
		newLabelSet = nsNew.Labels
		var oldLabelSet labels.Set
		oldLabelSet = nsOld.Labels
		// check 'replicate-to-matching' resources against new labels
		r.ReplicateToMatchingList.Range(func(sourceKey string, selector labels.Selector) bool {
			if selector.Matches(oldLabelSet) && !selector.Matches(newLabelSet) {
				obj, exists, err := r.Store.GetByKey(sourceKey)
				if err != nil {
					log.WithError(err).Error("error fetching object from store")
					return true
				} else if !exists {
					log.Warn("object not found in store")
					return true
				}
				// delete resource from the updated namespace
				logger.Infof("removed %s %s from %s", r.Kind, sourceKey, nsNew.Name)
				r.DeleteResourceInNamespaces(obj, &v1.NamespaceList{Items: []v1.Namespace{*nsNew}})
			}
			return true
		})

		// replicate resources to updated ns
		logger.Infof("labels of namespace %s changed, attempting to replicate %ss", nsNew.Name, r.Kind)
		r.NamespaceAdded(nsNew)
	}
}

// ResourceAdded checks resources with ReplicateTo or ReplicateFromAnnotation annotation
func (r *GenericReplicator) ResourceAdded(obj interface{}) {
	objectMeta := MustGetObject(obj)
	sourceKey := MustGetKey(objectMeta)
	logger := log.WithField("kind", r.Kind).WithField("resource", sourceKey)

	ctx := context.Background()

	if replicas, ok := r.DependencyMap[sourceKey]; ok {
		logger.Debugf("objectMeta %s has %d dependents", sourceKey, len(replicas))
		if err := r.updateDependents(obj, replicas); err != nil {
			logger.WithError(err).Error("failed to update cache")
		}
	}

	annotations := objectMeta.GetAnnotations()

	// Match resources with "replicate-from" annotation
	if source, ok := annotations[ReplicateFromAnnotation]; ok {
		if err := r.resourceAddedReplicateFrom(source, obj); err != nil {
			logger.WithError(err).Error("could not copy from source")
		}

		return
	}

	// Match resources with "replicate-to" annotation
	if namespacePatterns, ok := annotations[ReplicateTo]; ok {
		r.ReplicateToList.Store(sourceKey, struct{}{})

		namespacesFromStore := namespaceWatcher.NamespaceStore.List()
		namespaces := make([]v1.Namespace, len(namespacesFromStore))
		for i, ns := range namespacesFromStore {
			namespaces[i] = *ns.(*v1.Namespace)
		}
		if err := r.replicateResourceToMatchingNamespaces(obj, namespacePatterns, namespaces); err != nil {
			logger.WithError(err).Errorf("could not replicate object to other namespaces")
		}
	} else {
		r.ReplicateToList.Delete(sourceKey)
	}

	// Match resources with "replicate-to-matching" annotations
	if namespaceSelectorString, ok := annotations[ReplicateToMatching]; ok {
		namespaceSelector, err := labels.Parse(namespaceSelectorString)
		if err != nil {
			r.ReplicateToMatchingList.Delete(sourceKey)
			logger.WithError(err).Error("failed to parse label selector")

			return
		}

		r.ReplicateToMatchingList.Store(sourceKey, namespaceSelector)

		if err := r.replicateResourceToMatchingNamespacesByLabel(ctx, obj, namespaceSelector); err != nil {
			logger.WithError(err).Error("error while replicating by label selector")
		}
	} else {
		r.ReplicateToMatchingList.Delete(sourceKey)
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

// resourceAddedReplicateFrom replicates resources with ReplicateTo annotation
func (r *GenericReplicator) replicateResourceToMatchingNamespaces(obj interface{}, nsPatternList string, namespaceList []v1.Namespace) error {
	cacheKey := MustGetKey(obj)
	logger := log.WithField("kind", r.Kind).WithField("source", cacheKey)

	logger.Infof("%s %s to be replicated to: [%s]", r.Kind, cacheKey, nsPatternList)

	replicateTo := r.getNamespacesToReplicate(MustGetObject(obj).GetNamespace(), nsPatternList, namespaceList)

	if replicated, err := r.replicateResourceToNamespaces(obj, replicateTo); err != nil {
		return errors.Wrapf(err, "Replicated %s to %d out of %d namespaces",
			cacheKey, len(replicated), len(replicateTo),
		)
	}

	return nil
}

func (r *GenericReplicator) replicateResourceToMatchingNamespacesByLabel(ctx context.Context, obj interface{}, selector labels.Selector) error {
	cacheKey := MustGetKey(obj)

	namespaces, err := r.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return errors.Wrap(err, "error while listing namespaces by selector")
	}

	if replicated, err := r.replicateResourceToNamespaces(obj, namespaces.Items); err != nil {
		return errors.Wrapf(err, "Replicated %s to %d out of %d namespaces",
			cacheKey, len(replicated), len(namespaces.Items),
		)
	}

	return nil
}

// getNamespacesToReplicate will check the provided filters and create a list of namespace into with to replicate the
// given object.
func (r *GenericReplicator) getNamespacesToReplicate(myNs string, patterns string, namespaces []v1.Namespace) []v1.Namespace {

	replicateTo := make([]v1.Namespace, 0)
	for _, namespace := range namespaces {
		for _, ns := range StringToPatternList(patterns) {
			if matched := ns.MatchString(namespace.Name); matched {
				if namespace.Name == myNs {
					// Don't replicate upon itself
					continue
				}
				replicateTo = append(replicateTo, namespace)
				break

			}
		}
	}
	return replicateTo
}

// replicateResourceToNamespaces will replicate the given object into target namespaces. It will return a list of
// Namespaces it was successful in replicating into
func (r *GenericReplicator) replicateResourceToNamespaces(obj interface{}, targets []v1.Namespace) (replicatedTo []v1.Namespace, err error) {
	cacheKey := MustGetKey(obj)

	for _, namespace := range targets {
		if innerErr := r.UpdateFuncs.ReplicateObjectTo(obj, &namespace); innerErr != nil {
			err = multierror.Append(err, errors.Wrapf(innerErr, "Failed to replicate %s %s -> %s: %v",
				r.Kind, cacheKey, namespace.Name, innerErr,
			))
		} else {
			replicatedTo = append(replicatedTo, namespace)
			logger := log.WithField("source", cacheKey)
			logger.Infof("Replicated %s to: %v", cacheKey, namespace.Name)
		}
	}

	return
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

	r.ReplicateToList.Delete(sourceKey)

}

func (r *GenericReplicator) ResourceDeletedReplicateTo(source interface{}) {
	sourceKey := MustGetKey(source)
	logger := log.WithField("kind", r.Kind).WithField("source", sourceKey)
	objMeta := MustGetObject(source)
	namespaceList, replicateTo := objMeta.GetAnnotations()[ReplicateTo]
	if replicateTo {
		filters := strings.Split(namespaceList, ",")
		list, err := r.Client.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			err = errors.Wrapf(err, "Failed to list namespaces: %v", err)
			logger.WithError(err).Errorf("Could not get namespaces: %+v", err)
		} else {
			r.DeleteResources(source, list, filters)
		}
	}

	// delete replicated resources in namespaces that match labels
	namespaceSelectorString, replicateToMatching := objMeta.GetAnnotations()[ReplicateToMatching]
	if replicateToMatching {
		namespaceSelector, err := labels.Parse(namespaceSelectorString)
		if err != nil {
			err = errors.Wrapf(err, "Failed parse namespace selector: %v", err)
			logger.WithError(err).Errorf("Could not get namespaces: %+v", err)
		} else {
			var namespaces *v1.NamespaceList
			namespaces, err = r.Client.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{LabelSelector: namespaceSelector.String()})
			if err != nil {
				err = errors.Wrapf(err, "Failed to list namespaces: %v", err)
				logger.WithError(err).Errorf("Could not get namespaces: %+v", err)
			} else {
				r.DeleteResourceInNamespaces(source, namespaces)
			}
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

// DeleteResourceInNamespaces deletes resources in a list of namespaces acquired by evaluating namespace labels
func (r *GenericReplicator) DeleteResourceInNamespaces(source interface{}, list *v1.NamespaceList) {
	for _, namespace := range list.Items {
		r.DeleteResource(namespace, source)
	}
}

func (r *GenericReplicator) DeleteResource(namespace v1.Namespace, source interface{}) {
	sourceKey := MustGetKey(source)

	logger := log.WithField("kind", r.Kind).WithField("source", sourceKey)
	objMeta := MustGetObject(source)

	if namespace.Name == objMeta.GetNamespace() {
		// Don't work upon itself
		return
	}
	targetLocation := fmt.Sprintf("%s/%s", namespace.Name, objMeta.GetName())
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
		s, err := r.UpdateFuncs.PatchDeleteDependent(sourceKey, target)
		if err != nil {
			logger.WithError(err).Warnf("could not patch dependent %s %s: %v", r.Kind, dependentKey, err)
			continue
		}
		if err := r.Store.Update(s); err != nil {
			logger.WithError(err).Errorf("Error updating store for %s %s: %v", r.Kind, MustGetKey(s), err)
		}
	}
}
