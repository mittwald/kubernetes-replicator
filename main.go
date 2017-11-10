package main

import "k8s.io/client-go/kubernetes"
import (
	"flag"
	"log"
	"time"

	"github.com/mittwald/kubernetes-replicator/replicate"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var f flags

func init() {
	var err error
	flag.StringVar(&f.Kubeconfig, "kubeconfig", "", "path to Kubernetes config file")
	flag.StringVar(&f.ResyncPeriodS, "resync-period", "30m", "resynchronization period")
	flag.Parse()

	f.ResyncPeriod, err = time.ParseDuration(f.ResyncPeriodS)
	if err != nil {
		panic(err)
	}
}

func main() {
	var config *rest.Config
	var err error
	var client kubernetes.Interface

	if f.Kubeconfig == "" {
		log.Printf("using in-cluster configuration")
		config, err = rest.InClusterConfig()
	} else {
		log.Printf("using configuration from '%s'", f.Kubeconfig)
		config, err = clientcmd.BuildConfigFromFlags("", f.Kubeconfig)
	}

	if err != nil {
		panic(err)
	}

	client = kubernetes.NewForConfigOrDie(config)

	go func() {
		repl := replicate.NewSecretReplicator(client, f.ResyncPeriod)
		repl.Run()
	}()

	repl := replicate.NewConfigMapReplicator(client, f.ResyncPeriod)
	repl.Run()
}
