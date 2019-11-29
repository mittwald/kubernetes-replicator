package main

import "time"

type flags struct {
	AnnotationsPrefix string
	Kubeconfig        string
	ResyncPeriodS     string
	ResyncPeriod      time.Duration
	StatusAddr        string
	AllowAll          bool
}
