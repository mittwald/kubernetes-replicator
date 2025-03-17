package common

import (
	"github.com/prometheus/client_golang/prometheus"
)

type ReplicatorMetrics struct {
	Kind             string
	OperationCounter *prometheus.CounterVec
}

type Operation string

const (
	Update Operation = "Update"
	Patch  Operation = "Patch"
	Create Operation = "Create"
	Delete Operation = "Delete"
)

func NewMetrics(reg prometheus.Registerer) *ReplicatorMetrics {
	m := &ReplicatorMetrics{
		OperationCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "kubernetes_replicator",
				Subsystem: "reconciliation",
				Name:      "operation_count",
				Help:      "Counter for each operation to change a resource",
			},
			[]string{"kind", "namespace", "name", "operation"},
		),
	}
	reg.MustRegister(m.OperationCounter)
	return m
}

func (self ReplicatorMetrics) WithKind(kind string) *ReplicatorMetrics {
	self.Kind = kind
	return &self
}

func (self *ReplicatorMetrics) OperationCounterInc(namespace string, name string, operation Operation) {
	self.OperationCounter.With(prometheus.Labels{"kind": self.Kind, "namespace": namespace, "name": name, "operation": string(operation)}).Inc()
}
