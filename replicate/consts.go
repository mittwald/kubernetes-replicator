package replicate

// Annotations that are used to control this controller's behaviour
const (
	ReplicateFromAnnotation         = "replicator.v1.mittwald.de/replicate-from"
	ReplicateToAnnotation           = "replicator.v1.mittwald.de/replicate-to"
	ReplicateOnceAnnotation         = "replicator.v1.mittwald.de/replicate-once"
	ReplicatedAtAnnotation          = "replicator.v1.mittwald.de/replicated-at"
	ReplicatedFromAnnotation        = "replicator.v1.mittwald.de/replicated-from"
	ReplicatedFromVersionAnnotation = "replicator.v1.mittwald.de/replicated-from-version"
	ReplicationAllowed              = "replicator.v1.mittwald.de/replication-allowed"
	ReplicationAllowedNamespaces    = "replicator.v1.mittwald.de/replication-allowed-namespaces"
)
