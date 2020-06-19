package replicate

import (
	"fmt"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"testing"
	"time"
)

func TestSecretReplicator(t *testing.T) {
	configFile := os.Getenv("KUBECONFIG")
	config, err := clientcmd.BuildConfigFromFlags("", configFile)
	require.NoError(t, err)

	client := kubernetes.NewForConfigOrDie(config)

	repl := NewSecretReplicator(client, 30 * time.Second, false)
	go repl.Run()

	time.Sleep(200 * time.Millisecond)

	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}
	_, err = client.CoreV1().Namespaces().Create(&ns)
	require.NoError(t, err)

	defer func() {
		_ = client.CoreV1().Namespaces().Delete(ns.Name, &metav1.DeleteOptions{})
	}()

	secrets := client.CoreV1().Secrets("test")

	t.Run("replicates from existing secret", func(t *testing.T) {
		source := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:  "source",
				Namespace: ns.Name,
				Annotations: map[string]string{
					ReplicationAllowed: "true",
					ReplicationAllowedNamespaces: ns.Name,
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"foo": []byte("Hello World"),
			},
		}

		target := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "target",
				Namespace: ns.Name,
				Annotations: map[string]string{
					ReplicateFromAnnotation: fmt.Sprintf("%s/%s", source.Namespace, source.Name),
				},
			},
			Type: corev1.SecretTypeOpaque,
		}

		_, err := secrets.Create(&source)
		require.NoError(t, err)

		_, err = secrets.Create(&target)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		updTarget, err := secrets.Get(target.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, []byte("Hello World"), updTarget.Data["foo"])
	})

	t.Run("replicates keeps originally present values", func(t *testing.T) {
		source := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:  "source3",
				Namespace: ns.Name,
				Annotations: map[string]string{
					ReplicationAllowed: "true",
					ReplicationAllowedNamespaces: ns.Name,
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"foo": []byte("Hello World"),
			},
		}

		target := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "target3",
				Namespace: ns.Name,
				Annotations: map[string]string{
					ReplicateFromAnnotation: fmt.Sprintf("%s/%s", source.Namespace, source.Name),
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"bar": []byte("Hello Bar"),
			},
		}

		_, err := secrets.Create(&source)
		require.NoError(t, err)

		_, err = secrets.Create(&target)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		updTarget, err := secrets.Get(target.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, []byte("Hello World"), updTarget.Data["foo"])
		require.Equal(t, []byte("Hello Bar"), updTarget.Data["bar"])
	})

	t.Run("replication removes keys removed from source secret", func(t *testing.T) {
		source := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:  "source2",
				Namespace: ns.Name,
				Annotations: map[string]string{
					ReplicationAllowed: "true",
					ReplicationAllowedNamespaces: ns.Name,
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"foo": []byte("Hello Foo"),
				"bar": []byte("Hello Bar"),
			},
		}

		target := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "target2",
				Namespace: ns.Name,
				Annotations: map[string]string{
					ReplicateFromAnnotation: fmt.Sprintf("%s/%s", source.Namespace, source.Name),
				},
			},
			Type: corev1.SecretTypeOpaque,
		}

		_, err := secrets.Create(&source)
		require.NoError(t, err)

		_, err = secrets.Create(&target)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		updTarget, err := secrets.Get(target.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, []byte("Hello Foo"), updTarget.Data["foo"])

		_, err = secrets.Patch(source.Name, types.JSONPatchType, []byte(`[{"op": "remove", "path": "/data/foo"}]`))
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		updTarget, err = secrets.Get(target.Name, metav1.GetOptions{})
		require.NoError(t, err)

		_, hasFoo := updTarget.Data["foo"]
		require.False(t, hasFoo)
	})

	t.Run("replication does not remove original values", func(t *testing.T) {
		source := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:  "source4",
				Namespace: ns.Name,
				Annotations: map[string]string{
					ReplicationAllowed: "true",
					ReplicationAllowedNamespaces: ns.Name,
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"foo": []byte("Hello Foo"),
				"bar": []byte("Hello Bar"),
			},
		}

		target := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "target4",
				Namespace: ns.Name,
				Annotations: map[string]string{
					ReplicateFromAnnotation: fmt.Sprintf("%s/%s", source.Namespace, source.Name),
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"bar": []byte("Hello Bar"),
			},
		}

		_, err := secrets.Create(&source)
		require.NoError(t, err)

		_, err = secrets.Create(&target)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		updTarget, err := secrets.Get(target.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, []byte("Hello Foo"), updTarget.Data["foo"])

		_, err = secrets.Patch(source.Name, types.JSONPatchType, []byte(`[{"op": "remove", "path": "/data/foo"}]`))
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		updTarget, err = secrets.Get(target.Name, metav1.GetOptions{})
		require.NoError(t, err)

		_, hasFoo := updTarget.Data["foo"]
		require.False(t, hasFoo)
		require.Equal(t, []byte("Hello Bar"), updTarget.Data["bar"])
	})
}