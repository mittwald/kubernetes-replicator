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
	StripAnnotations                = "replicator.v1.mittwald.de/strip-annotations"
)
