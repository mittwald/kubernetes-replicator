package main

import "time"

type flags struct {
	Kubeconfig               string
	ResyncPeriodS            string
	ResyncPeriod             time.Duration
	StatusAddr               string
	AllowAll                 bool
	LogLevel                 string
	LogFormat                string
	ReplicateSecrets         bool
	ReplicateConfigMaps      bool
	ReplicateRoles           bool
	ReplicateRoleBindings    bool
	ReplicateServices        bool
	ReplicateServiceAccounts bool
}
