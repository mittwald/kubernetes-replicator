package serviceaccount

import (
	"context"
	"encoding/json"
	"fmt"
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

// NewReplicator creates a new serviceaccount replicator
func NewReplicator(client kubernetes.Interface, resyncPeriod time.Duration, allowAll bool, annotationsFilter *common.AnnotationsFilter) common.Replicator {
	repl := Replicator{
		GenericReplicator: common.NewGenericReplicator(common.ReplicatorConfig{
			Kind:         "ServiceAccount",
			ObjType:      &corev1.ServiceAccount{},
			AllowAll:     allowAll,
			ResyncPeriod: resyncPeriod,
			Client:       client,
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				return client.CoreV1().ServiceAccounts("").List(context.TODO(), lo)
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				return client.CoreV1().ServiceAccounts("").Watch(context.TODO(), lo)
			},
			AnnotationsFilter: annotationsFilter,
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
	source := sourceObj.(*corev1.ServiceAccount)
	target := targetObj.(*corev1.ServiceAccount)

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
	targetCopy.ImagePullSecrets = source.ImagePullSecrets

	log.Infof("updating target %s/%s", target.Namespace, target.Name)

	targetCopy.Annotations[common.ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	targetCopy.Annotations[common.ReplicatedFromVersionAnnotation] = source.ResourceVersion

	s, err := r.Client.CoreV1().ServiceAccounts(target.Namespace).Update(context.TODO(), targetCopy, metav1.UpdateOptions{})
	if err != nil {
		err = errors.Wrapf(err, "Failed updating target %s/%s", target.Namespace, targetCopy.Name)
	} else if err = r.Store.Update(s); err != nil {
		err = errors.Wrapf(err, "Failed to update cache for %s/%s: %v", target.Namespace, targetCopy, err)
	}

	return err
}

// ReplicateObjectTo copies the whole object to target namespace
func (r *Replicator) ReplicateObjectTo(sourceObj interface{}, target *v1.Namespace) error {
	source := sourceObj.(*corev1.ServiceAccount)
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

	var targetCopy *corev1.ServiceAccount
	if exists {
		targetObject := targetResource.(*corev1.ServiceAccount)
		targetVersion, ok := targetObject.Annotations[common.ReplicatedFromVersionAnnotation]
		sourceVersion := source.ResourceVersion

		if ok && targetVersion == sourceVersion {
			logger.Debugf("ServiceAccount %s is already up-to-date", common.MustGetKey(targetObject))
			return nil
		}

		targetCopy = targetObject.DeepCopy()
	} else {
		targetCopy = new(corev1.ServiceAccount)
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
	targetCopy.ImagePullSecrets = source.ImagePullSecrets
	targetCopy.Annotations[common.ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	targetCopy.Annotations[common.ReplicatedFromVersionAnnotation] = source.ResourceVersion

	var obj interface{}

	if exists {
		if err == nil {
			logger.Debugf("Updating existing serviceAccount %s/%s", target.Name, targetCopy.Name)
			obj, err = r.Client.CoreV1().ServiceAccounts(target.Name).Update(context.TODO(), targetCopy, metav1.UpdateOptions{})
		}
	} else {
		if err == nil {
			logger.Debugf("Creating a new serviceAccount %s/%s", target.Name, targetCopy.Name)
			obj, err = r.Client.CoreV1().ServiceAccounts(target.Name).Create(context.TODO(), targetCopy, metav1.CreateOptions{})
		}
	}
	if err != nil {
		return errors.Wrapf(err, "Failed to update serviceAccount %s/%s", target.Name, targetCopy.Name)
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

	targetObject, ok := target.(*corev1.ServiceAccount)
	if !ok {
		err := errors.Errorf("bad type returned from Store: %T", target)
		return nil, err
	}

	patch := []common.JSONPatchOperation{{Operation: "remove", Path: "/imagePullSecrets"}}
	patchBody, err := json.Marshal(&patch)

	if err != nil {
		return nil, errors.Wrapf(err, "error while building patch body for serviceAccount %s: %v", dependentKey, err)

	}

	logger.Debugf("clearing dependent serviceAccount %s", dependentKey)
	logger.Tracef("patch body: %s", string(patchBody))

	s, err := r.Client.CoreV1().ServiceAccounts(targetObject.Namespace).Patch(context.TODO(), targetObject.Name, types.JSONPatchType, patchBody, metav1.PatchOptions{})
	if err != nil {
		return nil, errors.Wrapf(err, "error while patching serviceAccount %s: %v", dependentKey, err)
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

	object := targetResource.(*corev1.ServiceAccount)
	logger.Debugf("Deleting %s", targetLocation)
	if err := r.Client.CoreV1().ServiceAccounts(object.Namespace).Delete(context.TODO(), object.Name, metav1.DeleteOptions{}); err != nil {
		return errors.Wrapf(err, "Failed deleting %s: %v", targetLocation, err)
	}
	return nil
}
