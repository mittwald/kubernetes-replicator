package common

// Annotations that are used to control this Controller's behaviour
const (
	ReplicateFromAnnotation         = "replicator.v1.mittwald.de/replicate-from"
	ReplicatedAtAnnotation          = "replicator.v1.mittwald.de/replicated-at"
	ReplicatedFromVersionAnnotation = "replicator.v1.mittwald.de/replicated-from-version"
	ReplicatedKeysAnnotation        = "replicator.v1.mittwald.de/replicated-keys"
	ReplicationAllowed              = "replicator.v1.mittwald.de/replication-allowed"
	ReplicationAllowedNamespaces    = "replicator.v1.mittwald.de/replication-allowed-namespaces"
	ReplicateTo                     = "replicator.v1.mittwald.de/replicate-to"
	ReplicateToMatching             = "replicator.v1.mittwald.de/replicate-to-matching"
	KeepOwnerReferences             = "replicator.v1.mittwald.de/keep-owner-references"
	StripLabels                     = "replicator.v1.mittwald.de/strip-labels"

	// ReplicatedFromSourceAnnotation is set by the replicator on every object it creates through push replication
	// ("replicate-to" / "replicate-to-matching"). It holds the "<namespace>/<name>" key of the source object. Push
	// replication only modifies or deletes objects whose annotation names the pushing source.
	ReplicatedFromSourceAnnotation = "replicator.v1.mittwald.de/replicated-from-source"

	// AllowPushFrom can be set on a Namespace. It contains a comma separated list of regular expressions; objects
	// from source namespaces matching one of them may be pushed into the annotated namespace.
	AllowPushFrom = "replicator.v1.mittwald.de/allow-push-from"
)
