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

type targetPattern struct {
	namesapce *regexp.Regexp
	name      string
}

func (targetPattern pattern) MatchString(target string) bool {
	parts := strings.SplitN("/", 2)
	return len(parts) == 2 && parts[1] == pattern.name && pattern.namesapce.MatchString(parts[0])
}

func (targetPattern pattern) Match(object *metav1.ObjectMeta) bool {
	return object.Name == pattern.name && pattern.namesapce.MatchString(object.Namespace)
}

func (targetPattern pattern) Targets(namespaces []string) []string {
	suffix := "/" + name
	targets := []string{}
	for _, ns := range namespaces {
		if pattern.namesapce.MatchString(ns) {
			targets = append(targets, ns+suffix)
		}
	}
	return targets
}

type replicatorProps struct {
	Name                string
	allowAll            bool
	client              kubernetes.Interface

	objectStore         cache.Store
	objectController    cache.Controller

	namespaceStore      cache.Store
	namespaceController cache.Controller

	dependencyMap       map[string][]string
	targetMap           map[string][]string
	targetPatternMap    map[string][]targetPattern
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
func (r *replicatorProps) isReplicatedBy(object *metav1.ObjectMeta, sourceObject *metav1.ObjectMeta) (bool, error) {
	// make sure that the target object was created from the source
	if annotationFrom, ok := object.Annotations[ReplicatedByAnnotation]; !ok {
		return false, fmt.Errorf("target %s/%s was not replicated",
			object.Namespace, object.Name)

	} else if annotationFrom != fmt.Sprintf("%s/%s", sourceObject.Namespace, sourceObject.Name) {
		return false, fmt.Errorf("target %s/%s was not replicated from %s/%s",
			object.Namespace, object.Name, sourceObject.Namespace, sourceObject.Name)
	}

	return true, nil
}

func (r *replicatorProps) isReplicatedTo(object *metav1.ObjectMeta, targetObject *metav1.ObjectMeta) (bool, error) {
	targets, targetPatterns, err := getReplicationTargets(object)
	if err != nil {
		return false, err
	}

	key := fmt.Sprintf("%s/%s", targetObject.Namespace, targetObject.Name)
	for _, t := range targets {
		if t == key {
			return true, nil
		}
	}

	for _, p := range targetPatterns {
		if p.Match(targetObject) {
			return true, nil
		}
	}

	return false, nil

	// return false, fmt.Error("source %s/%s is not replated to %s",
	// 	object.Namespace, object.Name, key)
}

var validName = regexp.MustCompile(`^[0-9a-z.-]*$`)

func (r *replicatorProps) getReplicationTargets(object *metav1.ObjectMeta) ([]string, []targetPattern, error) {
	if annotationTo, okTo := object.Annotations[ReplicateToAnnotation]
	if annotationToNs, okToNs := object.Annotations[ReplicateToNamespacesAnnotation]
	if !okTo && !okToNs {
		return nil, nil, nil
	}

	targets := []string{}
	targetPatterns := []targetPattern{}
	seen := []string{}
	var names, namespaces, qualified := map[string]bool

	if !okTo {
		names = map[string]bool{object.Name: true}
	} else {
		names = map[string]bool{}
		qualified = map[string]bool{}
		for _ n := range strings.Split(annotationTo, ",") {
			if strings.ContainsAny(n, "/") {
				qualified[n] = true
			} else if n != "" {
				names[n] = true
			}
		}
	}

	if !okToNs {
		namespaces = map[string]bool{object.Namespace: true}
	} else {
		namespaces = map[string]bool{}
		for _ ns := range strings.Split(annotationToNs, ",") {
			if n != "" {
				namespaces[ns] = true
			}
		}
	}

	for ns := range namespaces {
		if validName.MatchString(ns) {
			ns = ns + "/"
			for n := range names {
				n = ns + n
				if !seen[n] {
					seen[n] = true
					targets = append(targets, n)
				}
			}
		} else if regex, err := regexp.Compile(`^(?:`+ns+`)$`); err == nil {
			ns = ns + "/"
			for n := range names {
				full := ns + n
				if !seen[full] {
					seen[full] = true
					targetPatterns = append(targetPatterns, targetPattern{regex, n})
				}
			}
		} else {
			return nil, nil, fmt.Errorf("source %s/%s has compilation error on annotation %s (%s): %s",
				object.Namespace, object.Name, ReplicateToNamespacesAnnotation, ns, err)
		}
	}

	for q := range qualified {
		if seen[q] {
		// check that there is exactly one "/"
		} if qs := strings.SplitN(q, "/", 3); len(qs) != 2 {
			return nil, nil, fmt.Errorf("source %s/%s has invalid path on annotation %s (%s)",
				object.Namespace, object.Name, ReplicateToAnnotation, q)
		// check if the namesapce is a pattern
		} else if ns, n := qs[0], qs[1]; validName.MatchString(ns) {
			targets = append(targets, q)
		// check that the pattern compiles
		} else if pattern, err := regexp.Compile(`^(?:`+ns+`)$`); err == nil {
			targetPatterns = append(targetPatterns, targetPattern{regex, n})
		// raise compilation error
		} else {
			return nil, nil, fmt.Errorf("source %s/%s has compilation error on annotation %s (%s): %s",
				object.Namespace, object.Name, ReplicateToAnnotation, ns, err)
		}
	}

	return targets, targetPatterns, nil
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
