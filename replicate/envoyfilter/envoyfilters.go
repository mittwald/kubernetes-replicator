package envoyfilter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mittwald/kubernetes-replicator/replicate/common"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	networkingv1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	"istio.io/client-go/pkg/clientset/versioned"
)

type Replicator struct {
	*common.GenericReplicator
}

// NewReplicator creates a new envoyfilter replicator
func NewReplicator(client kubernetes.Interface, resyncPeriod time.Duration, allowAll bool, istioClient versioned.Interface) common.Replicator {
	repl := Replicator{
		GenericReplicator: common.NewGenericReplicator(common.ReplicatorConfig{
			Kind:         "EnvoyFilter",
			ObjType:      &networkingv1alpha3.EnvoyFilter{},
			AllowAll:     allowAll,
			ResyncPeriod: resyncPeriod,
			Client:       client,
			IstioClient:  istioClient,
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				return istioClient.NetworkingV1alpha3().EnvoyFilters("").List(context.TODO(), lo)
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				return istioClient.NetworkingV1alpha3().EnvoyFilters("").Watch(context.TODO(), lo)
			},
		}),
	}
	repl.UpdateFuncs = common.UpdateFuncs{
		ReplicateDataFrom:        repl.ReplicateDataFrom,
		ReplicateObjectTo:        repl.ReplicateObjectTo,
		PatchDeleteDependent:     repl.PatchDeleteDependent,
		DeleteReplicatedResource: repl.DeleteReplicatedResource,
	}

	return &repl
}

func (r *Replicator) ReplicateDataFrom(sourceObj interface{}, targetObj interface{}) error {
	source := sourceObj.(*networkingv1alpha3.EnvoyFilter)
	target := targetObj.(*networkingv1alpha3.EnvoyFilter)

	logger := log.
		WithField("kind", r.Kind).
		WithField("source", common.MustGetKey(source)).
		WithField("target", common.MustGetKey(target))

	// make sure replication is allowed
	if ok, err := r.IsReplicationPermitted(&target.ObjectMeta, &source.ObjectMeta); !ok {
		return errors.Wrapf(err, "replication of target %s is not permitted", common.MustGetKey(source))
	}

	targetVersion, ok := target.Annotations[common.ReplicatedFromVersionAnnotation]
	sourceVersion := source.ResourceVersion

	if ok && targetVersion == sourceVersion {
		logger.Debugf("target %s/%s is already up-to-date", target.Namespace, target.Name)
		return nil
	}

	targetCopy := target.DeepCopy()

	log.Infof("updating target %s/%s", target.Namespace, target.Name)

	targetCopy.Annotations[common.ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	targetCopy.Annotations[common.ReplicatedFromVersionAnnotation] = source.ResourceVersion
	targetCopy.Spec = source.Spec

	s, err := r.IstioClient.NetworkingV1alpha3().EnvoyFilters(target.Namespace).Update(context.TODO(), targetCopy, metav1.UpdateOptions{})
	if err != nil {
		err = errors.Wrapf(err, "Failed updating target %s/%s", target.Namespace, targetCopy.Name)
	} else if err = r.Store.Update(s); err != nil {
		err = errors.Wrapf(err, "Failed to update cache for %s/%s: %v", target.Namespace, targetCopy, err)
	}

	return err
}

// ReplicateObjectTo copies the whole object to target namespace
func (r *Replicator) ReplicateObjectTo(sourceObj interface{}, target *v1.Namespace) error {
	source := sourceObj.(*networkingv1alpha3.EnvoyFilter)
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

	var targetCopy *networkingv1alpha3.EnvoyFilter
	if exists {
		targetObject := targetResource.(*networkingv1alpha3.EnvoyFilter)
		targetVersion, ok := targetObject.Annotations[common.ReplicatedFromVersionAnnotation]
		sourceVersion := source.ResourceVersion

		if ok && targetVersion == sourceVersion {
			logger.Debugf("EnvoyFilter %s is already up-to-date", common.MustGetKey(targetObject))
			return nil
		}

		targetCopy = targetObject.DeepCopy()
	} else {
		targetCopy = new(networkingv1alpha3.EnvoyFilter)
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

	targetCopy.Name = source.Name
	targetCopy.Labels = labelsCopy
	targetCopy.Spec = source.Spec
	targetCopy.Annotations[common.ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	targetCopy.Annotations[common.ReplicatedFromVersionAnnotation] = source.ResourceVersion

	var obj interface{}

	if exists {
		if err == nil {
			logger.Debugf("Updating existing envoyFilter %s/%s", target.Name, targetCopy.Name)
			obj, err = r.IstioClient.NetworkingV1alpha3().EnvoyFilters(target.Name).Update(context.TODO(), targetCopy, metav1.UpdateOptions{})
		}
	} else {
		if err == nil {
			logger.Debugf("Creating a new envoyFilter %s/%s", target.Name, targetCopy.Name)
			obj, err = r.IstioClient.NetworkingV1alpha3().EnvoyFilters(target.Name).Create(context.TODO(), targetCopy, metav1.CreateOptions{})
		}
	}
	if err != nil {
		return errors.Wrapf(err, "Failed to update envoyFilter %s/%s", target.Name, targetCopy.Name)
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

	targetObject, ok := target.(*networkingv1alpha3.EnvoyFilter)
	if !ok {
		err := errors.Errorf("bad type returned from Store: %T", target)
		return nil, err
	}

	patch := []common.JSONPatchOperation{{Operation: "remove", Path: "/spec"}}
	patchBody, err := json.Marshal(&patch)

	if err != nil {
		return nil, errors.Wrapf(err, "error while building patch body for envoyfilter %s: %v", dependentKey, err)

	}

	logger.Debugf("clearing dependent envoyfilter %s", dependentKey)
	logger.Tracef("patch body: %s", string(patchBody))

	s, err := r.IstioClient.NetworkingV1alpha3().EnvoyFilters(targetObject.Namespace).Patch(context.TODO(), targetObject.Name, types.JSONPatchType, patchBody, metav1.PatchOptions{})
	if err != nil {
		return nil, errors.Wrapf(err, "error while patching envoyfilter %s: %v", dependentKey, err)

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

	object := targetResource.(*networkingv1alpha3.EnvoyFilter)
	logger.Debugf("Deleting %s", targetLocation)
	if err := r.IstioClient.NetworkingV1alpha3().EnvoyFilters(object.Namespace).Delete(context.TODO(), object.Name, metav1.DeleteOptions{}); err != nil {
		return errors.Wrapf(err, "Failed deleting %s: %v", targetLocation, err)
	}
	return nil
}
