package replicate

import (
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"regexp"
	"strconv"
	"strings"
)

type replicatorProps struct {
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
// Returns true if replication is allowed. If replication is not allowed returns false with
// error message
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

func (r *replicatorProps) isReplicatedFrom(object *metav1.ObjectMeta, sourceObject *metav1.ObjectMeta) (bool, error) {
	if ok := annotationRefersTo(object, ReplicateFromAnnotation, sourceObject); !ok {
		return false, fmt.Errorf("annotation of dependent %s/%s changed", object.Namespace, object.Name)
	}

	return true, nil
}

func (r *replicatorProps) canReplicateTo(object *metav1.ObjectMeta, targetObject *metav1.ObjectMeta) (bool, error) {

	// check for once annotation
	if annotationOnce, ok := object.Annotations[ReplicateOnceAnnotation]; ok {
		if once, err := strconv.ParseBool(annotationOnce); err != nil {
			return false, fmt.Errorf("source %s/%s has illformed annotation %s: %s",
				object.Namespace, object.Name, ReplicateOnceAnnotation, err)
		} else if once && targetObject != nil {
			return false, fmt.Errorf("target %s/%s already exists, replication skipped",
				object.Namespace, object.Name)
		}
	}

	if object == nil {
		return true, nil
	}

	return r.isReplicatedTo(object, targetObject)
}

func (r *replicatorProps) isReplicatedTo(object *metav1.ObjectMeta, targetObject *metav1.ObjectMeta) (bool, error) {
	// make sure that the target object was created from the source
	if annotationFrom, ok := targetObject.Annotations[ReplicatedFromAnnotation]; !ok {
		return false, fmt.Errorf("target %s/%s was not replicated",
			targetObject.Namespace, targetObject.Name)
	} else if annotationFrom != fmt.Sprintf("%s/%s", object.Namespace, object.Name) {
		return false, fmt.Errorf("target %s/%s was not replicated from %s/%s",
			targetObject.Namespace, targetObject.Name, object.Namespace, object.Name)
	}

	return true, nil
}

func resolveAnnotation(object *metav1.ObjectMeta, annotation string) (string, bool) {
	if val, ok := object.Annotations[annotation]; !ok {
		return "", false
	} else if strings.ContainsAny(val, "/") {
		return val, true
	} else {
		return fmt.Sprintf("%s/%s", object.Namespace, val), true
	}
}

func annotationRefersTo(object *metav1.ObjectMeta, annotation string, reference *metav1.ObjectMeta) bool {
	if val, ok := object.Annotations[annotation]; !ok {
		return false
	} else if v := strings.SplitN(val, "/", 2); len(v) == 2 {
		return v[0] == reference.Namespace && v[1] == reference.Name
	} else {
		return object.Namespace == reference.Namespace && val == reference.Name
	}
}
