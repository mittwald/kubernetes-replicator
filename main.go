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
	"github.com/mittwald/kubernetes-replicator/replicate/service"
	"github.com/mittwald/kubernetes-replicator/replicate/serviceaccount"

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
	flag.BoolVar(&f.ReplicateSecrets, "replicate-secrets", true, "Enable replication of secrets")
	flag.BoolVar(&f.ReplicateConfigMaps, "replicate-configmaps", true, "Enable replication of config maps")
	flag.BoolVar(&f.ReplicateRoles, "replicate-roles", true, "Enable replication of roles")
	flag.BoolVar(&f.ReplicateRoleBindings, "replicate-role-bindings", true, "Enable replication of role bindings")
	flag.BoolVar(&f.ReplicateServiceAccounts, "replicate-service-accounts", true, "Enable replication of service accounts")
	flag.BoolVar(&f.ReplicateServices, "replicate-services", true, "Enable replication of services")
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
	var enabledReplicators []common.Replicator

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

	if f.ReplicateSecrets {
		secretRepl := secret.NewReplicator(client, f.ResyncPeriod, f.AllowAll)
		go secretRepl.Run()
		enabledReplicators = append(enabledReplicators, secretRepl)
	}

	if f.ReplicateConfigMaps {
		configMapRepl := configmap.NewReplicator(client, f.ResyncPeriod, f.AllowAll)
		go configMapRepl.Run()
		enabledReplicators = append(enabledReplicators, configMapRepl)
	}

	if f.ReplicateRoles {
		roleRepl := role.NewReplicator(client, f.ResyncPeriod, f.AllowAll)
		go roleRepl.Run()
		enabledReplicators = append(enabledReplicators, roleRepl)
	}

	if f.ReplicateRoleBindings {
		roleBindingRepl := rolebinding.NewReplicator(client, f.ResyncPeriod, f.AllowAll)
		go roleBindingRepl.Run()
		enabledReplicators = append(enabledReplicators, roleBindingRepl)
	}

	if f.ReplicateServiceAccounts {
		serviceAccountRepl := serviceaccount.NewReplicator(client, f.ResyncPeriod, f.AllowAll)
		go serviceAccountRepl.Run()
		enabledReplicators = append(enabledReplicators, serviceAccountRepl)
	}

	if f.ReplicateServices {
		serviceRepl := service.NewReplicator(client, f.ResyncPeriod, f.AllowAll)
		go serviceRepl.Run()
		enabledReplicators = append(enabledReplicators, serviceRepl)
	}

	h := liveness.Handler{
		Replicators: enabledReplicators,
	}

	log.Infof("starting liveness monitor at %s", f.StatusAddr)

	http.Handle("/healthz", &h)
	http.Handle("/readyz", &h)
	err = http.ListenAndServe(f.StatusAddr, nil)
	if err != nil {
		log.Fatal(err)
	}
}
