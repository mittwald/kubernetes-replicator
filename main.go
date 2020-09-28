package main

import (
	"flag"
	"net/http"
	"strings"
	"time"

	"github.com/mittwald/kubernetes-replicator/replicate/common"
	"github.com/mittwald/kubernetes-replicator/replicate/configmap"
	"github.com/mittwald/kubernetes-replicator/replicate/role"
	"github.com/mittwald/kubernetes-replicator/replicate/rolebinding"
	"github.com/mittwald/kubernetes-replicator/replicate/secret"

	log "github.com/sirupsen/logrus"

	"github.com/mittwald/kubernetes-replicator/liveness"
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
	flag.StringVar(&f.LogLevel, "log-level", "info", "Log level (trace, debug, info, warn, error)")
	flag.StringVar(&f.LogFormat, "log-format", "plain", "Log format (plain, json)")
	flag.BoolVar(&f.AllowAll, "allow-all", false, "allow replication of all secrets (CAUTION: only use when you know what you're doing)")
	flag.Parse()

	switch strings.ToUpper(strings.TrimSpace(f.LogLevel)) {
	case "TRACE":
		log.SetLevel(log.TraceLevel)
	case "DEBUG":
		log.SetLevel(log.DebugLevel)
	case "WARN", "WARNING":
		log.SetLevel(log.WarnLevel)
	case "ERROR":
		log.SetLevel(log.ErrorLevel)
	case "FATAL":
		log.SetLevel(log.FatalLevel)
	case "PANIC":
		log.SetLevel(log.PanicLevel)
	default:
		log.SetLevel(log.InfoLevel)
	}
	if strings.ToUpper(strings.TrimSpace(f.LogFormat)) == "JSON" {
		log.SetFormatter(&log.JSONFormatter{})
	}

	f.ResyncPeriod, err = time.ParseDuration(f.ResyncPeriodS)
	if err != nil {
		panic(err)
	}

	log.Debugf("using flag values %#v", f)
}

func main() {

	var config *rest.Config
	var err error
	var client kubernetes.Interface

	if f.Kubeconfig == "" {
		log.Info("using in-cluster configuration")
		config, err = rest.InClusterConfig()
	} else {
		log.Infof("using configuration from '%s'", f.Kubeconfig)
		config, err = clientcmd.BuildConfigFromFlags("", f.Kubeconfig)
	}

	if err != nil {
		panic(err)
	}

	client = kubernetes.NewForConfigOrDie(config)

	secretRepl := secret.NewReplicator(client, f.ResyncPeriod, f.AllowAll)
	configMapRepl := configmap.NewReplicator(client, f.ResyncPeriod, f.AllowAll)
	roleRepl := role.NewReplicator(client, f.ResyncPeriod, f.AllowAll)
	roleBindingRepl := rolebinding.NewReplicator(client, f.ResyncPeriod, f.AllowAll)

	go secretRepl.Run()

	go configMapRepl.Run()

	go roleRepl.Run()

	go roleBindingRepl.Run()

	h := liveness.Handler{
		Replicators: []common.Replicator{secretRepl, configMapRepl, roleRepl, roleBindingRepl},
	}

	log.Infof("starting liveness monitor at %s", f.StatusAddr)

	http.Handle("/healthz", &h)
	err = http.ListenAndServe(f.StatusAddr, nil)
	if err != nil {
		log.Fatal(err)
	}
}
