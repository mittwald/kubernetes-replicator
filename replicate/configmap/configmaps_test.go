package configmap

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mittwald/kubernetes-replicator/replicate/common"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	waitFor = 3 * time.Second
	tick    = 20 * time.Millisecond
	// settle is the time given to the replicator before asserting that something did NOT happen
	settle = 500 * time.Millisecond
)

func namespace(name string, labels, annotations map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, Annotations: annotations}}
}

// newFakeClient returns a fake clientset that assigns resource versions (which the replicator relies on) and counts
// config map updates.
func newFakeClient(updates *int64, objects ...runtime.Object) *fake.Clientset {
	client := fake.NewSimpleClientset(objects...)

	var resourceVersion int64 = 100
	client.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		var obj runtime.Object
		switch a := action.(type) {
		case k8stesting.CreateAction:
			obj = a.GetObject()
		case k8stesting.UpdateAction:
			obj = a.GetObject()
			if a.GetResource().Resource == "configmaps" {
				atomic.AddInt64(updates, 1)
			}
		}
		if obj != nil {
			if meta, ok := obj.(metav1.Object); ok {
				meta.SetResourceVersion(strconv.FormatInt(atomic.AddInt64(&resourceVersion, 1), 10))
			}
		}
		return false, nil, nil
	})

	client.Resources = []*metav1.APIResourceList{{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "namespaces", Kind: "Namespace", Namespaced: false},
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
		},
	}}

	return client
}

// TestConfigMapReplicatorSecurity runs a config map replicator against a fake API server. All cases share one
// replicator, because the namespace watcher is a process-wide singleton.
func TestConfigMapReplicatorSecurity(t *testing.T) {
	var updates int64
	client := newFakeClient(&updates,
		namespace("trusted", nil, nil),
		namespace("trusted2", nil, nil),
		namespace("tenant-a", map[string]string{"push-test": "yes"}, nil),
		namespace("tenant-b", nil, nil),
		namespace("kube-system", map[string]string{"push-test": "yes"}, nil),
		namespace("protected", nil, nil),
		namespace("consenting", map[string]string{"push-test": "yes"}, map[string]string{common.AllowPushFrom: "tenant-a"}),
		// A source and an up-to-date replica pushed by an earlier version of the replicator, both existing at
		// startup. The fake lists objects sorted by key, so the source is handled before the replica is cached.
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy-startup", Namespace: "trusted", ResourceVersion: "50",
				Annotations: map[string]string{common.ReplicateTo: "trusted2"}},
			Data: map[string]string{"foo": "bar"},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy-startup", Namespace: "trusted2", ResourceVersion: "51",
				Annotations: map[string]string{
					common.ReplicatedFromVersionAnnotation: "50",
					common.ReplicatedKeysAnnotation:        "foo",
				}},
			Data: map[string]string{"foo": "bar"},
		},
	)

	filter, err := common.NewNamespaceFilter([]string{"protected"})
	require.NoError(t, err)
	policy, err := common.NewPushPolicy([]string{"trusted.*"})
	require.NoError(t, err)

	repl := NewReplicator(client, 0, false, false, filter, policy)

	// Let the namespace informer sync before the config map informer starts, as it usually does in a cluster. Then
	// the initial namespace events are handled before any push source is known.
	require.Eventually(t, func() bool {
		for _, action := range client.Actions() {
			if action.GetVerb() == "watch" && action.GetResource().Resource == "namespaces" {
				return true
			}
		}
		return false
	}, waitFor, tick)
	time.Sleep(200 * time.Millisecond)

	go repl.Run()
	require.Eventually(t, repl.Synced, waitFor, tick)
	time.Sleep(200 * time.Millisecond) // let the watches start

	ctx := context.TODO()
	create := func(t *testing.T, cm *corev1.ConfigMap) {
		_, err := client.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	get := func(ns, name string) (*corev1.ConfigMap, error) {
		return client.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	}
	exists := func(ns, name string) func() bool {
		return func() bool {
			_, err := get(ns, name)
			return err == nil
		}
	}
	requireNotFound := func(t *testing.T, ns, name string) {
		_, err := get(ns, name)
		require.True(t, apierrors.IsNotFound(err), "expected %s/%s not to exist, got %v", ns, name, err)
	}

	t.Run("pull replication requires consent of the source", func(t *testing.T) {
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "tenant-b"},
			Data:       map[string]string{"dsn": "secret"},
		})
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "stolen", Namespace: "tenant-a",
				Annotations: map[string]string{common.ReplicateFromAnnotation: "tenant-b/private"}},
		})

		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "tenant-b", Annotations: map[string]string{
				common.ReplicationAllowed:           "true",
				common.ReplicationAllowedNamespaces: "tenant-a|protected",
			}},
			Data: map[string]string{"url": "https://example.com"},
		})
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "copy", Namespace: "tenant-a",
				Annotations: map[string]string{common.ReplicateFromAnnotation: "tenant-b/shared"}},
		})
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "copy", Namespace: "protected",
				Annotations: map[string]string{common.ReplicateFromAnnotation: "tenant-b/shared"}},
		})

		require.Eventually(t, func() bool {
			cm, err := get("tenant-a", "copy")
			return err == nil && cm.Data["url"] == "https://example.com"
		}, waitFor, tick)

		time.Sleep(settle)
		stolen, err := get("tenant-a", "stolen")
		require.NoError(t, err)
		require.Empty(t, stolen.Data)

		// pull targets in excluded namespaces are not written to
		protected, err := get("protected", "copy")
		require.NoError(t, err)
		require.Empty(t, protected.Data)
	})

	t.Run("push replication from an untrusted namespace needs consent of the target namespace", func(t *testing.T) {
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "pushed", Namespace: "tenant-a",
				Annotations: map[string]string{common.ReplicateTo: "kube-system,tenant-b,consenting"}},
			Data: map[string]string{"foo": "bar"},
		})
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "pushed-by-label", Namespace: "tenant-a",
				Annotations: map[string]string{common.ReplicateToMatching: "push-test=yes"}},
			Data: map[string]string{"foo": "bar"},
		})

		require.Eventually(t, exists("consenting", "pushed"), waitFor, tick)
		require.Eventually(t, exists("consenting", "pushed-by-label"), waitFor, tick)
		time.Sleep(settle)

		replica, err := get("consenting", "pushed")
		require.NoError(t, err)
		require.Equal(t, "tenant-a/pushed", replica.Annotations[common.ReplicatedFromSourceAnnotation])

		requireNotFound(t, "kube-system", "pushed")
		requireNotFound(t, "tenant-b", "pushed")
		requireNotFound(t, "kube-system", "pushed-by-label")
	})

	t.Run("push replication from a trusted namespace skips excluded namespaces", func(t *testing.T) {
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "everywhere", Namespace: "trusted",
				Annotations: map[string]string{common.ReplicateTo: ".*"}},
			Data: map[string]string{"foo": "bar"},
		})

		for _, ns := range []string{"trusted2", "tenant-a", "tenant-b", "kube-system", "consenting"} {
			require.Eventually(t, exists(ns, "everywhere"), waitFor, tick, ns)
		}
		time.Sleep(settle)
		requireNotFound(t, "protected", "everywhere")
	})

	t.Run("push replication does not overwrite or delete objects it did not create", func(t *testing.T) {
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "app-settings", Namespace: "tenant-b"},
			Data:       map[string]string{"url": "legit"},
		})
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "app-settings", Namespace: "trusted",
				Annotations: map[string]string{common.ReplicateTo: "tenant-a,tenant-b"}},
			Data: map[string]string{"url": "https://attacker.example"},
		})

		require.Eventually(t, exists("tenant-a", "app-settings"), waitFor, tick)
		time.Sleep(settle)

		victim, err := get("tenant-b", "app-settings")
		require.NoError(t, err)
		require.Equal(t, "legit", victim.Data["url"])
		require.NotContains(t, victim.Annotations, common.ReplicatedFromSourceAnnotation)

		require.NoError(t, client.CoreV1().ConfigMaps("trusted").Delete(ctx, "app-settings", metav1.DeleteOptions{}))

		// the own replica is removed, the pre-existing object stays
		require.Eventually(t, func() bool {
			_, err := get("tenant-a", "app-settings")
			return apierrors.IsNotFound(err)
		}, waitFor, tick)
		victim, err = get("tenant-b", "app-settings")
		require.NoError(t, err)
		require.Equal(t, "legit", victim.Data["url"])
	})

	t.Run("push replication takes over legacy replicas but not replicas of other sources", func(t *testing.T) {
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "tenant-b",
				Annotations: map[string]string{common.ReplicatedFromVersionAnnotation: "1"}},
			Data: map[string]string{"foo": "old"},
		})
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "trusted",
				Annotations: map[string]string{common.ReplicateTo: "tenant-b,consenting"}},
			Data: map[string]string{"foo": "new"},
		})

		require.Eventually(t, func() bool {
			cm, err := get("tenant-b", "legacy")
			return err == nil && cm.Data["foo"] == "new" &&
				cm.Annotations[common.ReplicatedFromSourceAnnotation] == "trusted/legacy"
		}, waitFor, tick)
		require.Eventually(t, exists("consenting", "legacy"), waitFor, tick)

		// tenant-a may push into "consenting", but not over the replica of trusted/legacy
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "tenant-a",
				Annotations: map[string]string{common.ReplicateTo: "consenting"}},
			Data: map[string]string{"foo": "attacker"},
		})
		time.Sleep(settle)

		replica, err := get("consenting", "legacy")
		require.NoError(t, err)
		require.Equal(t, "new", replica.Data["foo"])
		require.Equal(t, "trusted/legacy", replica.Annotations[common.ReplicatedFromSourceAnnotation])
	})

	t.Run("up-to-date legacy replicas record their source and are deleted with it", func(t *testing.T) {
		cmRepl := repl.(*Replicator)
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy-current", Namespace: "tenant-b",
				Annotations: map[string]string{
					common.ReplicatedFromVersionAnnotation: "4242",
					common.ReplicatedKeysAnnotation:        "foo",
				}},
			Data: map[string]string{"foo": "bar"},
		})
		require.Eventually(t, func() bool {
			_, ok, _ := cmRepl.Store.GetByKey("tenant-b/legacy-current")
			return ok
		}, waitFor, tick)

		// a source that has not changed since an earlier version of the replicator pushed it
		source := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy-current", Namespace: "trusted", ResourceVersion: "4242",
				Annotations: map[string]string{common.ReplicateTo: "tenant-b"}},
			Data: map[string]string{"foo": "bar"},
		}
		require.NoError(t, cmRepl.ReplicateObjectTo(source, namespace("tenant-b", nil, nil)))

		replica, err := get("tenant-b", "legacy-current")
		require.NoError(t, err)
		require.Equal(t, "trusted/legacy-current", replica.Annotations[common.ReplicatedFromSourceAnnotation])

		cmRepl.DeleteResource(*namespace("tenant-b", nil, nil), source)
		requireNotFound(t, "tenant-b", "legacy-current")
	})

	t.Run("legacy replicas that exist at startup record their source and are deleted with it", func(t *testing.T) {
		require.Eventually(t, func() bool {
			cm, err := get("trusted2", "legacy-startup")
			return err == nil && cm.Annotations[common.ReplicatedFromSourceAnnotation] == "trusted/legacy-startup"
		}, waitFor, tick)

		require.NoError(t, client.CoreV1().ConfigMaps("trusted").Delete(ctx, "legacy-startup", metav1.DeleteOptions{}))
		require.Eventually(t, func() bool {
			_, err := get("trusted2", "legacy-startup")
			return apierrors.IsNotFound(err)
		}, waitFor, tick)
	})

	t.Run("objects pushing into each other do not cause an update loop", func(t *testing.T) {
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "loop", Namespace: "trusted",
				Annotations: map[string]string{common.ReplicateTo: "trusted2"}},
			Data: map[string]string{"foo": "bar"},
		})
		require.Eventually(t, exists("trusted2", "loop"), waitFor, tick)

		replica, err := get("trusted2", "loop")
		require.NoError(t, err)
		replica.Annotations[common.ReplicateTo] = "trusted"
		_, err = client.CoreV1().ConfigMaps("trusted2").Update(ctx, replica, metav1.UpdateOptions{})
		require.NoError(t, err)

		time.Sleep(settle)
		before := atomic.LoadInt64(&updates)
		time.Sleep(time.Second)
		require.Less(t, atomic.LoadInt64(&updates)-before, int64(3))

		source, err := get("trusted", "loop")
		require.NoError(t, err)
		require.NotContains(t, source.Annotations, common.ReplicatedFromSourceAnnotation)
	})

	t.Run("only owner references to cluster-scoped owners are kept", func(t *testing.T) {
		create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "trusted",
				Annotations: map[string]string{
					common.ReplicateTo:         "tenant-a",
					common.KeepOwnerReferences: "true",
				},
				OwnerReferences: []metav1.OwnerReference{
					{APIVersion: "v1", Kind: "Namespace", Name: "trusted", UID: "uid-1"},
					{APIVersion: "v1", Kind: "ConfigMap", Name: "owner", UID: "uid-2"},
				},
			},
			Data: map[string]string{"foo": "bar"},
		})

		require.Eventually(t, exists("tenant-a", "owned"), waitFor, tick)
		replica, err := get("tenant-a", "owned")
		require.NoError(t, err)
		require.Len(t, replica.OwnerReferences, 1)
		require.Equal(t, "Namespace", replica.OwnerReferences[0].Kind)
	})
}
