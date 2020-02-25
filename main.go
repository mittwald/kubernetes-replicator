package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/mittwald/kubernetes-replicator/liveness"
	"github.com/mittwald/kubernetes-replicator/replicate"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var f flags

func init() {
	var err error
	flag.StringVar(&f.Kubeconfig, "kubeconfig", "", "path to Kubernetes config file")
	flag.StringVar(&f.ResyncPeriodS, "resync-period", "30m", "resynchronization period")
	flag.StringVar(&f.StatusAddr, "status-addr", ":9102", "listen address for status and monitoring server")
	flag.BoolVar(&f.AllowAll, "allow-all", false, "allow replication of all secrets (CAUTION: only use when you know what you're doing)")
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

	secretRepl := replicate.NewSecretReplicator(client, f.ResyncPeriod, f.AllowAll)
	configMapRepl := replicate.NewConfigMapReplicator(client, f.ResyncPeriod, f.AllowAll)
	roleRepl := replicate.NewRoleReplicator(client, f.ResyncPeriod, f.AllowAll)
	roleBindingRepl := replicate.NewRoleBindingReplicator(client, f.ResyncPeriod, f.AllowAll)

	go secretRepl.Run()

	go configMapRepl.Run()

	go roleRepl.Run()

	go roleBindingRepl.Run()

	h := liveness.Handler{
		Replicators: []replicate.Replicator{secretRepl, configMapRepl, roleRepl, roleBindingRepl},
	}

	log.Printf("starting liveness monitor at %s", f.StatusAddr)

	http.Handle("/healthz", &h)
	err = http.ListenAndServe(f.StatusAddr, nil)
	if err != nil {
		log.Fatal(err)
	}
}
