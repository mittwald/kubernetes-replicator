package main

import "time"

type Flags struct {
	Kubeconfig    string
	ResyncPeriodS string
	ResyncPeriod  time.Duration
}
