package common

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/labels"

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
	Kind            string
	Client          kubernetes.Interface
	ResyncPeriod    time.Duration
	AllowAll        bool
	SyncByContent   bool
	ListFunc        cache.ListFunc
	WatchFunc       cache.WatchFunc
	ObjType         runtime.Object
	NamespaceFilter *NamespaceFilter
	// PushPolicy decides which namespaces may push objects into other namespaces. A nil policy only permits
	// pushes into namespaces that accept them through the AllowPushFrom annotation.
	PushPolicy *PushPolicy
	// PushRequiresTrustedSource disables the AllowPushFrom namespace annotation for this kind: objects are only
	// pushed if the source namespace is allowed by the PushPolicy. Used for RBAC objects.
	PushRequiresTrustedSource bool
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
	DependentMap  map[string]string
	UpdateFuncs   UpdateFuncs

	// ReplicateToList is a set that caches the names of all secrets that have a
	// "replicate-to" annotation.
	ReplicateToList GenericMap[string, struct{}]

	// ReplicateToMatchingList is a set that caches the names of all secrets
	// that have a "replicate-to-matching" annotation.
	ReplicateToMatchingList GenericMap[string, labels.Selector]

	// ownerScopes caches whether owner reference kinds ("<apiVersion>/<kind>") are cluster-scoped.
	ownerScopes GenericMap[string, bool]
}

// NewGenericReplicator creates a new generic replicator
func NewGenericReplicator(config ReplicatorConfig) *GenericReplicator {
	repl := GenericReplicator{
		ReplicatorConfig:        config,
		DependencyMap:           make(map[string]map[string]interface{}),
		DependentMap:            make(map[string]string),
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

	repl.NamespaceFilter = config.NamespaceFilter

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
	allowed := MatchesAnyPattern(StringToPatternList(annotationAllowedNamespaces), object.Namespace)
	if allowed {
		log.Tracef("Namespace '%s' matches '%s' -- allowing replication", object.Namespace, annotationAllowedNamespaces)
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
	return r.Controller.HasSynced() &&
		namespaceWatcher.NamespaceController != nil && namespaceWatcher.NamespaceController.HasSynced()
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
		if nsNew.Annotations[AllowPushFrom] != nsOld.Annotations[AllowPushFrom] {
			// the namespace may accept pushes from other namespaces now
			logger.Infof("%s annotation of namespace %s changed, attempting to replicate %ss", AllowPushFrom, nsNew.Name, r.Kind)
			r.NamespaceAdded(nsNew)
		}
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
	source, ok := r.DependentMap[sourceKey]
	if ok {
		logger.Debugf("objectMeta %s has source %s", sourceKey, source)

		sourceObject, exists, err := r.Store.GetByKey(source)
		if err != nil {
			logger.Debugf("could not get source %s %s: %s", r.Kind, source, err)
			return
		} else if !exists {
			logger.Debugf("could not get source %s %s: does not exist", r.Kind, source)
			return
		}
		targetMap := map[string]interface{}{MustGetKey(obj): ""}
		if err := r.updateDependents(sourceObject, targetMap); err != nil {
			logger.WithError(err).
				Errorf("Failed to update cache for %s: %v", MustGetKey(objectMeta), err)
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

	if targetNamespace := MustGetObject(target).GetNamespace(); r.NamespaceFilter.ShouldExclude(targetNamespace) {
		logger.Infof("not replicating into %s: namespace %s is excluded", cacheKey, targetNamespace)
		return nil
	}

	v := strings.SplitN(sourceLocation, "/", 2)

	if len(v) < 2 {
		return errors.Errorf("Invalid source location expected '<namespace>/<name>', got '%s'", sourceLocation)
	}

	if _, ok := r.DependencyMap[sourceLocation]; !ok {
		r.DependencyMap[sourceLocation] = make(map[string]interface{})
	}

	r.DependencyMap[sourceLocation][cacheKey] = nil

	if _, ok := r.DependentMap[cacheKey]; !ok {
		r.DependentMap[cacheKey] = sourceLocation
	}

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

// replicateResourceToMatchingNamespaces replicates resources with ReplicateTo annotation
func (r *GenericReplicator) replicateResourceToMatchingNamespaces(obj interface{}, nsPatternList string, namespaceList []v1.Namespace) error {
	cacheKey := MustGetKey(obj)
	logger := log.WithField("kind", r.Kind).WithField("source", cacheKey)
	sourceNamespace := MustGetObject(obj).GetNamespace()

	logger.Infof("%s %s to be replicated to: [%s]", r.Kind, cacheKey, truncate(nsPatternList, 256))

	// Only namespaces that the source namespace may push into are considered. This also avoids compiling the
	// (user supplied) patterns at all if pushing from the source namespace is not permitted anywhere.
	candidates := r.pushTargets(sourceNamespace, namespaceList)
	if len(candidates) == 0 {
		logger.Infof("not replicating %s: pushing from namespace %s is not permitted into any of the %d candidate namespace(s); see the %s namespace annotation and the -push-allowed-source-namespaces flag",
			cacheKey, sourceNamespace, len(namespaceList), AllowPushFrom)
		return nil
	}

	patterns, err := ParseNamespacePatternList(nsPatternList)
	if err != nil {
		logger.WithError(err).Errorf("invalid %s annotation", ReplicateTo)
	}

	replicateTo := r.getNamespacesToReplicate(sourceNamespace, patterns, candidates)
	if skipped := len(r.getNamespacesToReplicate(sourceNamespace, patterns, namespaceList)) - len(replicateTo); skipped > 0 {
		logger.Warnf("not replicating %s into %d matching namespace(s): namespace excluded or pushing from namespace %s not permitted",
			cacheKey, skipped, sourceNamespace)
	}

	if replicated, err := r.replicateResourceToNamespaces(obj, replicateTo); err != nil {
		return errors.Wrapf(err, "Replicated %s to %d out of %d namespaces",
			cacheKey, len(replicated), len(replicateTo),
		)
	}

	return nil
}

func (r *GenericReplicator) replicateResourceToMatchingNamespacesByLabel(ctx context.Context, obj interface{}, selector labels.Selector) error {
	cacheKey := MustGetKey(obj)
	myNs := MustGetObject(obj).GetNamespace()

	namespaces, err := r.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return errors.Wrap(err, "error while listing namespaces by selector")
	}

	matching := make([]v1.Namespace, 0, len(namespaces.Items))
	for _, namespace := range namespaces.Items {
		if namespace.Name == myNs {
			// Don't replicate upon itself
			continue
		}
		matching = append(matching, namespace)
	}

	replicateTo := r.pushTargets(myNs, matching)
	if skipped := len(matching) - len(replicateTo); skipped > 0 {
		log.WithField("kind", r.Kind).WithField("source", cacheKey).
			Warnf("not replicating %s into %d matching namespace(s): namespace excluded or pushing from namespace %s not permitted",
				cacheKey, skipped, myNs)
	}

	if replicated, err := r.replicateResourceToNamespaces(obj, replicateTo); err != nil {
		return errors.Wrapf(err, "Replicated %s to %d out of %d namespaces",
			cacheKey, len(replicated), len(replicateTo),
		)
	}

	return nil
}

// getNamespacesToReplicate will check the provided (already compiled) patterns and create a list of namespace into
// with to replicate the given object.
func (r *GenericReplicator) getNamespacesToReplicate(myNs string, patterns []*regexp.Regexp, namespaces []v1.Namespace) []v1.Namespace {

	replicateTo := make([]v1.Namespace, 0)
	for _, namespace := range namespaces {
		if namespace.Name == myNs {
			// Don't replicate upon itself
			continue
		}
		if MatchesAnyPattern(patterns, namespace.Name) {
			replicateTo = append(replicateTo, namespace)
		}
	}
	return replicateTo
}

// replicateResourceToNamespaces will replicate the given object into target namespaces. It will return a list of
// Namespaces it was successful in replicating into
func (r *GenericReplicator) replicateResourceToNamespaces(obj interface{}, targets []v1.Namespace) (replicatedTo []v1.Namespace, err error) {
	cacheKey := MustGetKey(obj)
	sourceNamespace := MustGetObject(obj).GetNamespace()

	for _, namespace := range targets {
		if namespace.Name == sourceNamespace {
			// Don't replicate upon itself
			continue
		}

		if r.NamespaceFilter.ShouldExclude(namespace.Name) {
			log.WithFields(log.Fields{
				"kind":      r.Kind,
				"namespace": namespace.Name,
			}).Info("Skipping excluded namespace")
			continue
		}

		if permErr := r.IsPushPermitted(sourceNamespace, &namespace); permErr != nil {
			log.WithFields(log.Fields{
				"kind":   r.Kind,
				"source": cacheKey,
			}).WithError(permErr).Warnf("Not replicating %s to %s: push replication is not permitted", cacheKey, namespace.Name)
			continue
		}

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
		if r.isExcludedKey(dependentKey) {
			logger.Debugf("not updating dependent %s %s: namespace is excluded", r.Kind, dependentKey)
			continue
		}

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
		filters, err := ParseNamespacePatternList(namespaceList)
		if err != nil {
			logger.WithError(err).Errorf("invalid %s annotation", ReplicateTo)
		}
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

// DeleteResources deletes the replicas of source in all namespaces that match one of the given (compiled, strictly
// anchored) patterns. Only replicas that were created by push replication of source are deleted.
func (r *GenericReplicator) DeleteResources(source interface{}, list *v1.NamespaceList, filters []*regexp.Regexp) {
	for _, namespace := range list.Items {
		if MatchesAnyPattern(filters, namespace.Name) {
			r.DeleteResource(namespace, source)
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
	if r.NamespaceFilter.ShouldExclude(namespace.Name) {
		logger.Debugf("not deleting %s %s/%s: namespace is excluded", r.Kind, namespace.Name, objMeta.GetName())
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
	if !IsPushReplicaOf(MustGetObject(targetResource), sourceKey) {
		// Only delete objects that were created for this source. Anything else (objects that existed before, or
		// that belong to another source) is left alone.
		logger.Infof("not deleting %s %s: it was not replicated from %s", r.Kind, targetLocation, sourceKey)
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
		if r.isExcludedKey(dependentKey) {
			logger.Debugf("not patching dependent %s %s: namespace is excluded", r.Kind, dependentKey)
			continue
		}
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

// isExcludedKey returns true if the namespace of the object with the given "<namespace>/<name>" key is excluded from
// replication.
func (r *GenericReplicator) isExcludedKey(key string) bool {
	namespace, _, _ := strings.Cut(key, "/")
	return r.NamespaceFilter.ShouldExclude(namespace)
}
