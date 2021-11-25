package configmap

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mittwald/kubernetes-replicator/replicate/common"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

type Replicator struct {
	*common.GenericReplicator
}

// NewReplicator creates a new config map replicator
func NewReplicator(client kubernetes.Interface, resyncPeriod time.Duration, allowAll bool) common.Replicator {
	repl := Replicator{
		GenericReplicator: common.NewGenericReplicator(common.ReplicatorConfig{
			Kind:         "ConfigMap",
			ObjType:      &v1.ConfigMap{},
			AllowAll:     allowAll,
			ResyncPeriod: resyncPeriod,
			Client:       client,
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				return client.CoreV1().ConfigMaps("").List(context.TODO(), lo)
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				return client.CoreV1().ConfigMaps("").Watch(context.TODO(), lo)
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

// ReplicateDataFrom takes a source object and copies over data to target object
func (r *Replicator) ReplicateDataFrom(sourceObj interface{}, targetObj interface{}) error {
	source := sourceObj.(*v1.ConfigMap)
	target := targetObj.(*v1.ConfigMap)

	// make sure replication is allowed
	logger := log.
		WithField("kind", r.Kind).
		WithField("source", common.MustGetKey(source)).
		WithField("target", common.MustGetKey(target))

	targetVersion, ok := target.Annotations[common.ReplicatedFromVersionAnnotation]
	sourceVersion := source.ResourceVersion

	if ok && targetVersion == sourceVersion {
		logger.Debugf("target %s is already up-to-date", common.MustGetKey(target))
		return nil
	}

	targetCopy := target.DeepCopy()
	if targetCopy.Data == nil {
		targetCopy.Data = make(map[string]string)
	}

	prevKeys, hasPrevKeys := common.PreviouslyPresentKeys(&targetCopy.ObjectMeta)
	replicatedKeys := make([]string, 0)

	for key, value := range source.Data {
		targetCopy.Data[key] = value

		replicatedKeys = append(replicatedKeys, key)
		delete(prevKeys, key)
	}

	if source.BinaryData != nil {
		targetCopy.BinaryData = make(map[string][]byte)
		for key, value := range source.BinaryData {
			targetCopy.BinaryData[key] = value

			replicatedKeys = append(replicatedKeys, key)
			delete(prevKeys, key)
		}
	}

	if hasPrevKeys {
		for k := range prevKeys {
			logger.Debugf("removing previously present key %s: not present in source any more", k)
			delete(targetCopy.Data, k)
			delete(targetCopy.BinaryData, k)
		}
	}

	sort.Strings(replicatedKeys)

	logger.Infof("updating config map %s/%s", target.Namespace, target.Name)

	targetCopy.Annotations[common.ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	targetCopy.Annotations[common.ReplicatedFromVersionAnnotation] = source.ResourceVersion
	targetCopy.Annotations[common.ReplicatedKeysAnnotation] = strings.Join(replicatedKeys, ",")

	s, err := r.Client.CoreV1().ConfigMaps(target.Namespace).Update(context.TODO(), targetCopy, metav1.UpdateOptions{})
	if err != nil {
		err = errors.Wrapf(err, "Failed updating target %s/%s", target.Namespace, targetCopy.Name)
	} else if err = r.Store.Update(s); err != nil {
		err = errors.Wrapf(err, "Failed to update cache for %s/%s: %v", target.Namespace, targetCopy, err)
	}

	return err
}

// ReplicateObjectTo copies the whole object to target namespace
func (r *Replicator) ReplicateObjectTo(sourceObj interface{}, target *v1.Namespace) error {
	source := sourceObj.(*v1.ConfigMap)
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

	var resourceCopy *v1.ConfigMap
	if exists {
		targetObject := targetResource.(*v1.ConfigMap)
		targetVersion, ok := targetObject.Annotations[common.ReplicatedFromVersionAnnotation]
		sourceVersion := source.ResourceVersion

		if ok && targetVersion == sourceVersion {
			logger.Debugf("Secret %s is already up-to-date", common.MustGetKey(targetObject))
			return nil
		}

		resourceCopy = targetObject.DeepCopy()
	} else {
		resourceCopy = new(v1.ConfigMap)
	}

	keepOwnerReferences, ok := source.Annotations[common.KeepOwnerReferences]
	if ok && keepOwnerReferences == "true" {
		resourceCopy.OwnerReferences = source.OwnerReferences
	}

	if resourceCopy.Data == nil {
		resourceCopy.Data = make(map[string]string)
	}
	if resourceCopy.BinaryData == nil {
		resourceCopy.BinaryData = make(map[string][]byte)
	}
	if resourceCopy.Annotations == nil {
		resourceCopy.Annotations = make(map[string]string)
	}

	prevKeys, hasPrevKeys := common.PreviouslyPresentKeys(&resourceCopy.ObjectMeta)
	replicatedKeys := make([]string, 0)

	for key, value := range source.Data {
		resourceCopy.Data[key] = value

		replicatedKeys = append(replicatedKeys, key)
		delete(prevKeys, key)
	}
	for key, value := range source.BinaryData {
		newValue := make([]byte, len(value))
		copy(newValue, value)
		resourceCopy.BinaryData[key] = newValue

		replicatedKeys = append(replicatedKeys, key)
		delete(prevKeys, key)
	}

	if hasPrevKeys {
		for k := range prevKeys {
			logger.Debugf("removing previously present key %s: not present in source secret any more", k)
			delete(resourceCopy.Data, k)
		}
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

	sort.Strings(replicatedKeys)
	resourceCopy.Name = source.Name
	resourceCopy.Labels = labelsCopy
	resourceCopy.Annotations[common.ReplicatedAtAnnotation] = time.Now().Format(time.RFC3339)
	resourceCopy.Annotations[common.ReplicatedFromVersionAnnotation] = source.ResourceVersion
	resourceCopy.Annotations[common.ReplicatedKeysAnnotation] = strings.Join(replicatedKeys, ",")

	var obj interface{}
	if exists {
		logger.Debugf("Updating existing secret %s/%s", target.Name, resourceCopy.Name)
		obj, err = r.Client.CoreV1().ConfigMaps(target.Name).Update(context.TODO(), resourceCopy, metav1.UpdateOptions{})
	} else {
		logger.Debugf("Creating a new secret secret %s/%s", target.Name, resourceCopy.Name)
		obj, err = r.Client.CoreV1().ConfigMaps(target.Name).Create(context.TODO(), resourceCopy, metav1.CreateOptions{})
	}
	if err != nil {
		return errors.Wrapf(err, "Failed to update secret %s/%s", target.Name, resourceCopy.Name)
	}

	if err := r.Store.Update(obj); err != nil {
		return errors.Wrapf(err, "Failed to update cache for %s/%s", target.Name, resourceCopy)
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

	targetObject, ok := target.(*v1.ConfigMap)
	if !ok {
		err := errors.Errorf("bad type returned from Store: %T", target)
		return nil, err
	}

	patch := []common.JSONPatchOperation{{Operation: "remove", Path: "/data"}}
	patchBody, err := json.Marshal(&patch)

	if err != nil {
		return nil, errors.Wrapf(err, "error while building patch body for confimap %s: %v", dependentKey, err)

	}

	logger.Debugf("clearing dependent config map %s", dependentKey)
	logger.Tracef("patch body: %s", string(patchBody))

	s, err := r.Client.CoreV1().ConfigMaps(targetObject.Namespace).Patch(context.TODO(), targetObject.Name, types.JSONPatchType, patchBody, metav1.PatchOptions{})
	if err != nil {
		return nil, errors.Wrapf(err, "error while patching secret %s: %v", dependentKey, err)

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

	object := targetResource.(*v1.ConfigMap)
	resourceKeys := make([]string, 0)
	resourceKeys = append(resourceKeys, common.GetKeysFromBinaryMap(object.BinaryData)...)
	resourceKeys = append(resourceKeys, common.GetKeysFromStringMap(object.Data)...)
	sort.Strings(resourceKeys)

	if strings.Join(resourceKeys, ",") == object.Annotations[common.ReplicatedKeysAnnotation] {
		logger.Debugf("Deleting %s", targetLocation)
		if err := r.Client.CoreV1().ConfigMaps(object.Namespace).Delete(context.TODO(), object.Name, metav1.DeleteOptions{}); err != nil {
			return errors.Wrapf(err, "Failed deleting %s: %v", targetLocation, err)
		}
	} else {
		var patch []common.JSONPatchOperation
		exists := make(map[string]struct{})
		for _, value := range resourceKeys {
			exists[value] = struct{}{}
		}
		for _, val := range strings.Split(object.Annotations[common.ReplicatedKeysAnnotation], ",") {
			if _, ok := exists[val]; ok {
				patch = append(patch, common.JSONPatchOperation{Operation: "remove", Path: fmt.Sprintf("/data/%s", val)})
			}
		}
		patch = append(patch, common.JSONPatchOperation{Operation: "remove", Path: fmt.Sprintf("/metadata/annotations/%s", common.JSONPatchPathEscape(common.ReplicatedKeysAnnotation))})

		patchBody, err := json.Marshal(&patch)
		if err != nil {
			return errors.Wrapf(err, "error while building patch body for confimap %s: %v", object, err)
		}

		s, err := r.Client.CoreV1().ConfigMaps(object.Namespace).Patch(context.TODO(), object.Name, types.JSONPatchType, patchBody, metav1.PatchOptions{})
		if err != nil {
			return errors.Wrapf(err, "error while patching secret %s: %v", s, err)

		}

		logger.Debugf("Not deleting %s since it contains other keys then replicated.", targetLocation)
	}

	return nil
}
