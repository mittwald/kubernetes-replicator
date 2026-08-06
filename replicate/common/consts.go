package common

// Annotations that are used to control this Controller's behaviour
const (
	ReplicatorPrefix = "replicator.v1.mittwald.de"
)

var (
	ReplicateFromAnnotation         = ReplicatorPrefix + "/replicate-from"
	ReplicatedAtAnnotation          = ReplicatorPrefix + "/replicated-at"
	ReplicatedFromVersionAnnotation = ReplicatorPrefix + "/replicated-from-version"
	ReplicatedKeysAnnotation        = ReplicatorPrefix + "/replicated-keys"
	ReplicationAllowed              = ReplicatorPrefix + "/replication-allowed"
	ReplicationAllowedNamespaces    = ReplicatorPrefix + "/replication-allowed-namespaces"
	ReplicateTo                     = ReplicatorPrefix + "/replicate-to"
	ReplicateToMatching             = ReplicatorPrefix + "/replicate-to-matching"
	KeepOwnerReferences             = ReplicatorPrefix + "/keep-owner-references"
	StripLabels                     = ReplicatorPrefix + "/strip-labels"
	StripAnnotations                = ReplicatorPrefix + "/strip-annotations"
)
