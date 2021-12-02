package main

import "time"

type flags struct {
	Kubeconfig    string
	ResyncPeriodS string
	ResyncPeriod  time.Duration
	StatusAddr    string
	AllowAll      bool
	DisablePush   bool
	LogLevel      string
	LogFormat     string
}
