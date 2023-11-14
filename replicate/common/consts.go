package common

// Annotations that are used to control this Controller's behaviour
const (
	Prefix = "replicator.v1.mittwald.de"
)

var (
	ReplicateFromAnnotation         = Prefix + "/replicate-from"
	ReplicatedAtAnnotation          = Prefix + "/replicated-at"
	ReplicatedFromVersionAnnotation = Prefix + "/replicated-from-version"
	ReplicatedKeysAnnotation        = Prefix + "/replicated-keys"
	ReplicationAllowed              = Prefix + "/replication-allowed"
	ReplicationAllowedNamespaces    = Prefix + "/replication-allowed-namespaces"
	ReplicateTo                     = Prefix + "/replicate-to"
	ReplicateToMatching             = Prefix + "/replicate-to-matching"
	KeepOwnerReferences             = Prefix + "/keep-owner-references"
	StripLabels                     = Prefix + "/strip-labels"
	StripAnnotations                = Prefix + "/strip-annotations"
)
