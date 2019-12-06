package replicate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// pattern of a valid kubernetes name
var validName = regexp.MustCompile(`^[0-9a-z.-]*$`)

// a struct representing a pattern to match namespaces and generating targets
type targetPattern struct {
	namespace *regexp.Regexp
	name      string
}
// if the pattern matches the given target object
func (pattern targetPattern) Match(object *metav1.ObjectMeta) bool {
	return object.Name == pattern.name && pattern.namespace.MatchString(object.Namespace)
}
// if the pattern matches the given target path
func (pattern targetPattern) MatchString(target string) bool {
	parts := strings.SplitN(target, "/", 2)
	return len(parts) == 2 && parts[1] == pattern.name && pattern.namespace.MatchString(parts[0])
}
// if the pattern matches the given namespace, returns a target path in this namespace
func (pattern targetPattern) MatchNamespace(namespace string) string {
	if pattern.namespace.MatchString(namespace) {
		return fmt.Sprintf("%s/%s", namespace, pattern.name)
	} else {
		return ""
	}
}
// returns a slice of targets paths in the given namespaces when matching
func (pattern targetPattern) Targets(namespaces []string) []string {
	suffix := "/" + pattern.name
	targets := []string{}
	for _, ns := range namespaces {
		if pattern.namespace.MatchString(ns) {
			targets = append(targets, ns+suffix)
		}
	}
	return targets
}

type replicatorProps struct {
	// displayed name for the resources
	Name                string
	// when true, "allowed" annotations are ignored
	allowAll            bool
	// the kubernetes client to use
	client              kubernetes.Interface

	// the store and controller for all the objects to watch replicate
	objectStore         cache.Store
	objectController    cache.Controller

	// the store and controller for the namespaces
	namespaceStore      cache.Store
	namespaceController cache.Controller

	// a {source => targets} map for the "replicate-from" annotation
	targetsFrom         map[string][]string
	// a {source => targets} map for the "replicate-to" annotation
	targetsTo           map[string][]string

	// a {source => namespaces} map for which namespaces are targeted
	watchedNamespaces   map[string][]string
	// a {source => patterns} for the patterns matched over the namespaces
	patternNamespaces   map[string][]*regexp.Regexp
}

// Replicator describes the common interface that the secret and configmap
// replicators should adhere to
type Replicator interface {
	Start()
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


// Checks if the object is replicated to the target
// Returns an error only if the annotations are invalid
func (r *replicatorProps) isReplicatedTo(object *metav1.ObjectMeta, targetObject *metav1.ObjectMeta) (bool, error) {
	targets, targetPatterns, _, err := r.getReplicationTargets(object)
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

// Returns everything needed to compute the desired targets
// - targets: a slice of all fully qualified target. Items are unique, does not contain object itself
// - targetPatterns: a slice of targetPattern, using regex to identify if a namespace is matched
//                   two patterns can generate the same target, and even the object itself
// - patterns: a slice of regexp patterns, used identify a namespace can be concerned by this source
//             two patterns can match the same namespace
func (r *replicatorProps) getReplicationTargets(object *metav1.ObjectMeta) ([]string, []targetPattern, []*regexp.Regexp, error) {
	annotationTo, okTo := object.Annotations[ReplicateToAnnotation]
	annotationToNs, okToNs := object.Annotations[ReplicateToNamespacesAnnotation]
	if !okTo && !okToNs {
		return nil, nil, nil, nil
	}

	key := fmt.Sprintf("%s/%s", object.Name, object.Namespace)
	targets := []string{}
	targetPatterns := []targetPattern{}
	patterns := []*regexp.Regexp{}
	compiledPatterns := map[string]*regexp.Regexp{} // cache of patterns, to reuse them
	seen := map[string]bool{key: true} // which qualified paths have already been seen
	var names, namespaces, qualified map[string]bool // sets for names, namespaces, and qualified names
	// no target explecitely provided, assumed that targets will have the same name
	if !okTo {
		names = map[string]bool{object.Name: true}
	// split the targets, and check which one are qualified
	} else {
		names = map[string]bool{}
		qualified = map[string]bool{}
		for _, n := range strings.Split(annotationTo, ",") {
			if n == "" {
			// a qualified name, with a namespace part
			} else if strings.ContainsAny(n, "/") {
				qualified[n] = true
			// a valid name
			} else if validName.MatchString(n) {
				names[n] = true
			// raise error
			} else {
				return nil, nil, nil, fmt.Errorf("source %s has invalid name on annotation %s (%s)",
					key, ReplicateToAnnotation, n)
			}
		}
	}
	// no target namespace provided, assume that the namespace is the samed (or qualified in the name)
	if !okToNs {
		namespaces = map[string]bool{object.Namespace: true}
	// split the target namespaces
	} else {
		namespaces = map[string]bool{}
		for _, ns := range strings.Split(annotationToNs, ",") {
			if strings.ContainsAny(ns, "/") {
				return nil, nil, nil, fmt.Errorf("source %s has invalid namespace pattern on annotation %s (%s)",
					key, ReplicateToNamespacesAnnotation, ns)
			} else if ns != "" {
				namespaces[ns] = true
			}
		}
	}
	// join all the namespaces and names
	for ns := range namespaces {
		// this namespace is not a pattern
		if validName.MatchString(ns) {
			ns = ns + "/"
			for n := range names {
				n = ns + n
				if !seen[n] {
					seen[n] = true
					targets = append(targets, n)
				}
			}
		// this namespace is a pattern
		} else if pattern, err := regexp.Compile(`^(?:`+ns+`)$`); err == nil {
			compiledPatterns[ns] = pattern
			patterns = append(patterns, pattern)
			ns = ns + "/"
			for n := range names {
				full := ns + n
				if !seen[full] {
					seen[full] = true
					targetPatterns = append(targetPatterns, targetPattern{pattern, n})
				}
			}
		// raise compilation error
		} else {
			return nil, nil, nil, fmt.Errorf("source %s has compilation error on annotation %s (%s): %s",
				key, ReplicateToNamespacesAnnotation, ns, err)
		}
	}
	// for all the qualified names, check if the namespace part is a pattern
	for q := range qualified {
		if seen[q] {
		// check that there is exactly one "/"
		} else if qs := strings.SplitN(q, "/", 3); len(qs) != 2 {
			return nil, nil, nil, fmt.Errorf("source %s has invalid path on annotation %s (%s)",
				key, ReplicateToAnnotation, q)
		// check that the name part is valid
		} else if n := qs[1]; !validName.MatchString(n) {
			return nil, nil, nil, fmt.Errorf("source %s has invalid name on annotation %s (%s)",
				key, ReplicateToAnnotation, n)
		// check if the namespace is a pattern
		} else if ns := qs[0]; validName.MatchString(ns) {
			targets = append(targets, q)
		// check if this pattern is already compiled
		} else if pattern, ok := compiledPatterns[ns]; ok {
			targetPatterns = append(targetPatterns, targetPattern{pattern, n})
		// check that the pattern compiles
		} else if pattern, err := regexp.Compile(`^(?:`+ns+`)$`); err == nil {
			compiledPatterns[ns] = pattern
			patterns = append(patterns, pattern)
			targetPatterns = append(targetPatterns, targetPattern{pattern, n})
		// raise compilation error
		} else {
			return nil, nil, nil, fmt.Errorf("source %s has compilation error on annotation %s (%s): %s",
				key, ReplicateToAnnotation, ns, err)
		}
	}

	return targets, targetPatterns, patterns, nil
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
