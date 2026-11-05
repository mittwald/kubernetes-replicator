package common

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PushPolicy decides which source namespaces are trusted to push objects ("replicate-to" and
// "replicate-to-matching") into other namespaces.
//
// Push replication writes into other namespaces with the replicator's own (cluster-wide) permissions. It is therefore
// only performed if the source namespace is allowed by this policy, or if the target namespace explicitly accepts
// objects from the source namespace through the AllowPushFrom annotation.
type PushPolicy struct {
	AllowedSourceNamespaces []string
	compiled                []*regexp.Regexp
}

// NewPushPolicy creates a push policy from a list of regular expressions. Each expression has to match the complete
// name of a source namespace. An error is returned for invalid expressions.
func NewPushPolicy(patterns []string) (*PushPolicy, error) {
	policy := PushPolicy{}
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		re, err := CompileStrictRegex(pat)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid push source namespace pattern %q", pat)
		}
		policy.AllowedSourceNamespaces = append(policy.AllowedSourceNamespaces, pat)
		policy.compiled = append(policy.compiled, re)
	}
	return &policy, nil
}

// AllowsSourceNamespace returns true if objects from the given namespace may be pushed into any namespace. It is safe
// to call on a nil policy, which allows nothing.
func (p *PushPolicy) AllowsSourceNamespace(namespace string) bool {
	if p == nil {
		return false
	}
	return MatchesAnyPattern(p.compiled, namespace)
}

// IsPushPermitted checks whether objects from sourceNamespace may be pushed into the target namespace. Push
// replication is permitted if the source namespace is trusted by the push policy, or if the target namespace accepts
// pushes from the source namespace through its AllowPushFrom annotation (unless PushRequiresTrustedSource is set).
func (r *GenericReplicator) IsPushPermitted(sourceNamespace string, target *v1.Namespace) error {
	if r.PushPolicy.AllowsSourceNamespace(sourceNamespace) {
		return nil
	}

	if r.PushRequiresTrustedSource {
		return errors.Errorf("namespace %s is not allowed to push %ss: the namespace has to be allowed by the push policy",
			sourceNamespace, r.Kind)
	}

	if consent, ok := target.Annotations[AllowPushFrom]; ok {
		if MatchesAnyPattern(StringToPatternList(consent), sourceNamespace) {
			return nil
		}
	}

	return errors.Errorf(
		"namespace %s is not allowed to push into namespace %s (neither allowed by the push policy nor by the %s annotation of the target namespace)",
		sourceNamespace, target.Name, AllowPushFrom)
}

// pushTargets returns the namespaces from the given list that objects from sourceNamespace may be pushed into: it
// removes the source namespace itself, excluded namespaces and namespaces that push replication is not permitted
// into.
func (r *GenericReplicator) pushTargets(sourceNamespace string, namespaces []v1.Namespace) []v1.Namespace {
	targets := make([]v1.Namespace, 0, len(namespaces))
	for i := range namespaces {
		namespace := namespaces[i]
		if namespace.Name == sourceNamespace {
			// Don't replicate upon itself
			continue
		}
		if r.NamespaceFilter.ShouldExclude(namespace.Name) {
			continue
		}
		if err := r.IsPushPermitted(sourceNamespace, &namespace); err != nil {
			continue
		}
		targets = append(targets, namespace)
	}
	return targets
}

// CheckPushTarget verifies that an object that already exists in a target namespace may be overwritten by the push
// replication of the source with the key sourceKey ("<namespace>/<name>").
//
// The replicator only modifies objects that it created for this very source (recognized by the
// ReplicatedFromSourceAnnotation annotation). Objects that were replicated by a version of the replicator that did not
// record the source yet (they carry a ReplicatedFromVersionAnnotation annotation, but no source) are taken over.
// Objects that are pull replication targets or push replication sources themselves are never modified, which also
// prevents replication loops between objects that push into each other.
func CheckPushTarget(sourceKey string, target metav1.Object) error {
	targetKey := MustGetKey(target)
	annotations := target.GetAnnotations()

	if _, ok := annotations[ReplicateFromAnnotation]; ok {
		return errors.Errorf("refusing to overwrite %s with %s: target is a %s replication target", targetKey, sourceKey, ReplicateFromAnnotation)
	}
	if _, ok := annotations[ReplicateTo]; ok {
		return errors.Errorf("refusing to overwrite %s with %s: target is a replication source itself", targetKey, sourceKey)
	}
	if _, ok := annotations[ReplicateToMatching]; ok {
		return errors.Errorf("refusing to overwrite %s with %s: target is a replication source itself", targetKey, sourceKey)
	}

	if owner, ok := annotations[ReplicatedFromSourceAnnotation]; ok {
		if owner == sourceKey {
			return nil
		}
		return errors.Errorf("refusing to overwrite %s with %s: target is replicated from %s", targetKey, sourceKey, owner)
	}

	if _, ok := annotations[ReplicatedFromVersionAnnotation]; ok {
		log.WithField("source", sourceKey).WithField("target", targetKey).
			Infof("taking over %s, which was replicated by an earlier version of the replicator", targetKey)
		return nil
	}

	return errors.Errorf("refusing to overwrite %s with %s: target was not created by the replicator", targetKey, sourceKey)
}

// IsPushReplicaOf returns true if the object was created by push replication of the source with the key sourceKey.
func IsPushReplicaOf(object metav1.Object, sourceKey string) bool {
	owner, ok := object.GetAnnotations()[ReplicatedFromSourceAnnotation]
	return ok && owner == sourceKey
}

// PushedOwnerReferences returns the owner references of a source object that may be kept on a copy in another
// namespace ("keep-owner-references"). Owner references across namespaces are not supported by Kubernetes: a
// namespaced owner is looked up in the namespace of the dependent and the garbage collector deletes the dependent if
// it does not exist there. Therefore, only references to cluster-scoped owners are kept.
func (r *GenericReplicator) PushedOwnerReferences(refs []metav1.OwnerReference) []metav1.OwnerReference {
	var result []metav1.OwnerReference
	for _, ref := range refs {
		clusterScoped, err := r.isClusterScoped(ref.APIVersion, ref.Kind)
		if err != nil {
			log.WithField("kind", r.Kind).WithError(err).
				Warnf("dropping owner reference to %s %s: could not determine its scope", ref.Kind, ref.Name)
			continue
		}
		if !clusterScoped {
			log.WithField("kind", r.Kind).
				Debugf("dropping owner reference to namespaced %s %s: owners must be in the same namespace", ref.Kind, ref.Name)
			continue
		}
		result = append(result, ref)
	}
	return result
}

func (r *GenericReplicator) isClusterScoped(apiVersion, kind string) (bool, error) {
	cacheKey := apiVersion + "/" + kind
	if clusterScoped, ok := r.ownerScopes.Load(cacheKey); ok {
		return clusterScoped, nil
	}

	resources, err := r.Client.Discovery().ServerResourcesForGroupVersion(apiVersion)
	if err != nil {
		return false, err
	}
	for _, resource := range resources.APIResources {
		if resource.Kind == kind && !strings.Contains(resource.Name, "/") {
			r.ownerScopes.Store(cacheKey, !resource.Namespaced)
			return !resource.Namespaced, nil
		}
	}

	return false, fmt.Errorf("kind %s not found in %s", kind, apiVersion)
}

// ReplicaDeleteOptions returns delete options that make sure that only the given instance of an object is deleted,
// and not an object that was created with the same name in the meantime.
func ReplicaDeleteOptions(object metav1.Object) metav1.DeleteOptions {
	uid := object.GetUID()
	if uid == "" {
		return metav1.DeleteOptions{}
	}
	return metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
}

// ReplicaPatchPreconditions returns JSON patch "test" operations that make a patch of a replica fail if the object
// was replaced, or if it is not attributed to the same push replication source any more.
func ReplicaPatchPreconditions(object metav1.Object) []JSONPatchOperation {
	var ops []JSONPatchOperation
	if uid := object.GetUID(); uid != "" {
		ops = append(ops, JSONPatchOperation{Operation: "test", Path: "/metadata/uid", Value: string(uid)})
	}
	if owner, ok := object.GetAnnotations()[ReplicatedFromSourceAnnotation]; ok {
		ops = append(ops, JSONPatchOperation{
			Operation: "test",
			Path:      "/metadata/annotations/" + JSONPatchPathEscape(ReplicatedFromSourceAnnotation),
			Value:     owner,
		})
	}
	return ops
}
