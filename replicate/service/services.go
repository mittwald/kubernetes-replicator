package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/mittwald/kubernetes-replicator/replicate/common"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

type Replicator struct {
	*common.GenericReplicator
}

// NewReplicator creates a new service replicator
func NewReplicator(client kubernetes.Interface, resyncPeriod time.Duration, allowAll bool) common.Replicator {
	repl := Replicator{
		GenericReplicator: common.NewGenericReplicator(common.ReplicatorConfig{
			Kind:         "Service",
			ObjType:      &corev1.Service{},
			AllowAll:     allowAll,
			ResyncPeriod: resyncPeriod,
			Client:       client,
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				return client.CoreV1().Services("").List(context.TODO(), lo)
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				return client.CoreV1().Services("").Watch(context.TODO(), lo)
			},
		}),
	}
	repl.UpdateFuncs = common.UpdateFuncs{
		ReplicateObjectTo:        repl.ReplicateObjectTo,
		PatchDeleteDependent:     repl.PatchDeleteDependent,
		DeleteReplicatedResource: repl.DeleteReplicatedResource,
	}

	return &repl
}

// ReplicateObjectTo copies the whole object to target namespace
func (r *Replicator) ReplicateObjectTo(sourceObj interface{}, target *v1.Namespace) error {
	source := sourceObj.(*corev1.Service)
	targetLocation := fmt.Sprintf("%s/%s", target.Name, source.Name)

	logger := log.
		WithField("kind", r.Kind).
		WithField("source", common.MustGetKey(source)).
		WithField("target", targetLocation)

	targetResource, exists, err := r.Store.GetByKey(targetLocation)
	if err != nil {
		return errors.Wrapf(err, "Could not get %s from cache!", targetLocation)
	}
	logger.Infof("Checking if %s exists? %v", targetLocation, exists)

	var targetCopy *corev1.Service
	if exists {
		targetObject := targetResource.(*corev1.Service)
		targetVersion, ok := targetObject.Annotations[common.ReplicatedFromVersionAnnotation]
		sourceVersion := source.ResourceVersion

		if ok && targetVersion == sourceVersion {
			logger.Debugf("Service %s is already up-to-date", common.MustGetKey(targetObject))
			return nil
		}

		targetCopy = targetObject.DeepCopy()
	} else {
		targetCopy = new(corev1.Service)
	}

	keepOwnerReferences, ok := source.Annotations[common.KeepOwnerReferences]
	if ok && keepOwnerReferences == "true" {
		targetCopy.OwnerReferences = source.OwnerReferences
	}

	if targetCopy.Annotations == nil {
		targetCopy.Annotations = make(map[string]string)
	}

	labelsCopy := make(map[string]string)

	stripLabels, ok := source.Annotations[common.StripLabels]
	if !ok && stripLabels != "true" {
		if source.Labels != nil {
			for key, value := range source.Labels {
				labelsCopy[key] = value
			}
		}
	}

	annotationsCopy := make(map[string]string)
	// we strip annotations by default as they usually contain data for eg. loadbalancer controllers
	// a user has to set `"replicator.v1.mittwald.de/ strip-annotations = false"` to keep them
	stripAnnotations, ok := source.Annotations[common.StripAnnotations]
	if ok && stripAnnotations == "false" {
		if source.Annotations != nil {
			for key, value := range source.Annotations {
				annotationsCopy[key] = value
			}
		}
	}

	// we clean out .Spec and set our own
	newSpec := new(corev1.ServiceSpec)
	newSpec.Type = corev1.ServiceTypeExternalName

	// Get the full DNS name of the source service as cluster domains can vary
	serviceFQDN, err := getFullDNSName(source.Name, source.Namespace)
	if err != nil {
		return errors.Wrapf(err, "Failed to get DNS name for service %s/%s", source.Namespace, source.Name)
	}
	logger.Debugf("Resolved existing service %s/%s to %s", target.Name, targetCopy.Name, serviceFQDN)

	newSpec.ExternalName = serviceFQDN
	targetCopy.Name = source.Name
	targetCopy.Labels = labelsCopy
	targetCopy.Spec = *newSpec
	targetCopy.Annotations = annotationsCopy
	targetCopy.Annotations[common.ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	targetCopy.Annotations[common.ReplicatedFromVersionAnnotation] = source.ResourceVersion

	var obj interface{}

	if exists {
		if err == nil {
			logger.Debugf("Updating existing service %s/%s", target.Name, targetCopy.Name)
			obj, err = r.Client.CoreV1().Services(target.Name).Update(context.TODO(), targetCopy, metav1.UpdateOptions{})
		}
	} else {
		if err == nil {
			logger.Debugf("Creating a new service %s/%s", target.Name, targetCopy.Name)
			obj, err = r.Client.CoreV1().Services(target.Name).Create(context.TODO(), targetCopy, metav1.CreateOptions{})
		}
	}
	if err != nil {
		return errors.Wrapf(err, "Failed to update service %s/%s", target.Name, targetCopy.Name)
	}

	if err := r.Store.Update(obj); err != nil {
		return errors.Wrapf(err, "Failed to update cache for %s/%s", target.Name, targetCopy)
	}

	return nil
}

func (r *Replicator) PatchDeleteDependent(sourceKey string, target interface{}) (interface{}, error) {
	dependentKey := common.MustGetKey(target)
	logger := log.WithFields(log.Fields{
		"kind":   r.Kind,
		"source": sourceKey,
		"target": dependentKey,
	})

	targetObject, ok := target.(*corev1.Service)
	if !ok {
		err := errors.Errorf("bad type returned from Store: %T", target)
		return nil, err
	}

	patch := []common.JSONPatchOperation{{Operation: "remove", Path: "/imagePullSecrets"}}
	patchBody, err := json.Marshal(&patch)

	if err != nil {
		return nil, errors.Wrapf(err, "error while building patch body for service %s: %v", dependentKey, err)

	}

	logger.Debugf("clearing dependent service %s", dependentKey)
	logger.Tracef("patch body: %s", string(patchBody))

	s, err := r.Client.CoreV1().Services(targetObject.Namespace).Patch(context.TODO(), targetObject.Name, types.JSONPatchType, patchBody, metav1.PatchOptions{})
	if err != nil {
		return nil, errors.Wrapf(err, "error while patching service %s: %v", dependentKey, err)
	}
	return s, nil
}

// DeleteReplicatedResource deletes a resource replicated by ReplicateTo annotation
func (r *Replicator) DeleteReplicatedResource(targetResource interface{}) error {
	targetLocation := common.MustGetKey(targetResource)
	logger := log.WithFields(log.Fields{
		"kind":   r.Kind,
		"target": targetLocation,
	})

	object := targetResource.(*corev1.Service)
	logger.Debugf("Deleting %s", targetLocation)
	if err := r.Client.CoreV1().Services(object.Namespace).Delete(context.TODO(), object.Name, metav1.DeleteOptions{}); err != nil {
		return errors.Wrapf(err, "Failed deleting %s: %v", targetLocation, err)
	}
	return nil
}

// Function to determine the full DNS name of the service
func getFullDNSName(serviceName, namespace string) (string, error) {
	// Perform DNS lookup to get the IP address of the service
	ips, err := net.LookupHost(fmt.Sprintf("%s.%s", serviceName, namespace))
	if err != nil {
		// Return an empty string and the error if DNS lookup fails
		return "", err
	}

	// Check if the lookup returned at least one IP address
	if len(ips) == 0 {
		return "", fmt.Errorf("DNS lookup returned empty result")
	}

	// Perform reverse DNS lookup to get the full DNS name of the IP address
	names, err := net.LookupAddr(ips[0])
	if err != nil {
		return "", err
	}

	// Check if the reverse lookup returned at least one name
	if len(names) == 0 {
		return "", fmt.Errorf("reverse DNS lookup returned empty result")
	}

	// Return the first name from the reverse lookup result
	return names[0], nil
}
