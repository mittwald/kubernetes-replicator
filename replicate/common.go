package replicate

import (
	"fmt"
	semver "github.com/Masterminds/semver/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"regexp"
	"strconv"
	"strings"
)

type replicatorProps struct {
	Name       string

	client     kubernetes.Interface
	store      cache.Store
	controller cache.Controller
	allowAll   bool

	dependencyMap map[string][]string
	targetMap map[string]string
}

// Replicator describes the common interface that the secret and configmap
// replicators should adhere to
type Replicator interface {
	Run()
	Synced() bool
}

// Checks if replication is allowed in annotations of the source object
// It means that replication-allowes and replications-allowed-namespaces are correct
// Returns true if replication is allowed.
// If replication is not allowed returns false with error message
func (r *replicatorProps) isReplicationPermitted(object *metav1.ObjectMeta, sourceObject *metav1.ObjectMeta) (bool, error) {
	if r.allowAll {
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
			"source %s/%s does not allow replication in namespace %s. %s will not be replicated",
			sourceObject.Namespace, sourceObject.Name, object.Namespace, object.Name)
	}
	allowedNamespaces := strings.Split(annotationAllowedNamespaces, ",")
	allowed := false
	for _, ns := range allowedNamespaces {
		ns := strings.TrimSpace(ns)

		if matched, _ := regexp.MatchString(ns, object.Namespace); matched {
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

// Checks that update is needed in annotations of the target and source objects
// Returns true if update is needed
// If update is not needed returns false with error message
func (r *replicatorProps) needsUpdate(object *metav1.ObjectMeta, sourceObject *metav1.ObjectMeta) (bool, error) {
	// target was "replicated" from a delete source, or never replicated
	if targetVersion, ok := object.Annotations[ReplicatedFromVersionAnnotation]; !ok {
		return true, nil
	// target and source share the same version
	} else if ok && targetVersion == sourceObject.ResourceVersion {
		return false, fmt.Errorf("target %s/%s is already up-to-date", object.Namespace, object.Name)
	}

	hasOnce := false
	// no once annotation, nothing to check
	if annotationOnce, ok := sourceObject.Annotations[ReplicateOnceAnnotation]; !ok {
	// once annotation is not a boolean
	} else if once, err := strconv.ParseBool(annotationOnce); err != nil {
		return false, fmt.Errorf("source %s/%s has illformed annotation %s: %s",
			sourceObject.Namespace, sourceObject.Name, ReplicateOnceAnnotation, err)
	// once annotation is present
	} else if once {
		hasOnce = true
	}
	// no once annotation, nothing to check
	if annotationOnce, ok := object.Annotations[ReplicateOnceAnnotation]; !ok {
	// once annotation is not a boolean
	} else if once, err := strconv.ParseBool(annotationOnce); err != nil {
		return false, fmt.Errorf("target %s/%s has illformed annotation %s: %s",
			object.Namespace, object.Name, ReplicateOnceAnnotation, err)
	// once annotation is present
	} else if once {
		hasOnce = true
	}

	if !hasOnce {
	// no once version annotation in the source, only replicate once
	} else if annotationVersion, ok := sourceObject.Annotations[ReplicateOnceVersionAnnotation]; !ok {
	// once version annotation is not a valid version
	} else if sourceVersion, err := semver.NewVersion(annotationVersion); err != nil {
		return false, fmt.Errorf("source %s/%s has illformed annotation %s: %s",
			sourceObject.Namespace, sourceObject.Name, ReplicateOnceVersionAnnotation, err)
	// the source has a once version annotation but it is "0.0.0" anyway
	} else if version0, _ := semver.NewVersion("0"); sourceVersion.Equal(version0) {
	// no once version annotation in the target, should update
	} else if annotationVersion, ok := object.Annotations[ReplicateOnceVersionAnnotation]; !ok {
		hasOnce = false
	// once version annotation is not a valid version
	} else if targetVersion, err := semver.NewVersion(annotationVersion); err != nil {
		return false, fmt.Errorf("target %s/%s has illformed annotation %s: %s",
			object.Namespace, object.Name, ReplicateOnceVersionAnnotation, err)
	// source version is greatwe than source version, should update
	} else if sourceVersion.GreaterThan(targetVersion) {
		hasOnce = false
	// source version is not greater than target version
	} else {
		return false, fmt.Errorf("target %s/%s is already replicated once at version %s",
			object.Namespace, object.Name, sourceVersion)
	}

	if hasOnce {
		return false, fmt.Errorf("target %s/%s is already replicated once",
			object.Namespace, object.Name)
	}

	return true, nil
}

// Checks that replication from the source object to the target objects is allowed
// It means that the target object was created using replication of the same source
// Returns true if replication is allowed
// If replication is not allowed returns false with error message
func (r *replicatorProps) canReplicateTo(object *metav1.ObjectMeta, targetObject *metav1.ObjectMeta) (bool, error) {
	// make sure that the target object was created from the source
	if annotationFrom, ok := targetObject.Annotations[ReplicatedByAnnotation]; !ok {
		return false, fmt.Errorf("target %s/%s was not replicated",
			targetObject.Namespace, targetObject.Name)

	} else if annotationFrom != fmt.Sprintf("%s/%s", object.Namespace, object.Name) {
		return false, fmt.Errorf("target %s/%s was not replicated from %s/%s",
			targetObject.Namespace, targetObject.Name, object.Namespace, object.Name)
	}

	return true, nil
}

// Returns an annotation as "namespace/name" format
func resolveAnnotation(object *metav1.ObjectMeta, annotation string) (string, bool) {
	if val, ok := object.Annotations[annotation]; !ok {
		return "", false
	} else if strings.ContainsAny(val, "/") {
		return val, true
	} else {
		return fmt.Sprintf("%s/%s", object.Namespace, val), true
	}
}

// Returns true if the annotation from the object references the other object
func annotationRefersTo(object *metav1.ObjectMeta, annotation string, reference *metav1.ObjectMeta) bool {
	if val, ok := object.Annotations[annotation]; !ok {
		return false
	} else if v := strings.SplitN(val, "/", 2); len(v) == 2 {
		return v[0] == reference.Namespace && v[1] == reference.Name
	} else {
		return object.Namespace == reference.Namespace && val == reference.Name
	}
}
