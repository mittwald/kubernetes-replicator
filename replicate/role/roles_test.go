package role

import (
	"bytes"
	"context"
	"fmt"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mittwald/kubernetes-replicator/replicate/common"
	pkgerrors "github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

func namespacePrefix() string {
	//	Mon Jan 2 15:04:05 -0700 MST 2006
	return "test-repl-" + time.Now().Format("060102150405") + "-"
}

type EventHandlerFuncs struct {
	AddFunc    func(wg *sync.WaitGroup, obj interface{})
	UpdateFunc func(wg *sync.WaitGroup, oldObj, newObj interface{})
	DeleteFunc func(wg *sync.WaitGroup, obj interface{})
}

type PlainFormatter struct {
}

func (pf *PlainFormatter) Format(entry *log.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}

	b.WriteString(entry.Time.Format("15:04:05") + " ")
	b.WriteString(fmt.Sprintf("%-8s", strings.ToUpper(entry.Level.String())))
	b.WriteString(entry.Message)

	if val, ok := entry.Data[log.ErrorKey]; ok {
		b.WriteByte('\n')
		b.WriteString(fmt.Sprint(val))
	}

	b.WriteByte('\n')
	return b.Bytes(), nil
}

func TestRoleReplicator(t *testing.T) {

	log.SetLevel(log.TraceLevel)
	log.SetFormatter(&PlainFormatter{})

	kubeconfig := os.Getenv("KUBECONFIG")
	//is KUBECONFIG is not specified try to use the local KUBECONFIG or the in cluster config
	if len(kubeconfig) == 0 {
		if home := homeDir(); home != "" && home != "/root" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	require.NoError(t, err)

	prefix := namespacePrefix()
	client := kubernetes.NewForConfigOrDie(config)

	repl := NewReplicator(client, 60*time.Second, false, false)
	go repl.Run()

	time.Sleep(200 * time.Millisecond)

	ns := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: prefix + "test",
		},
	}
	_, err = client.CoreV1().Namespaces().Create(context.TODO(), &ns, metav1.CreateOptions{})
	require.NoError(t, err)

	ns2 := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: prefix + "test2",
			Labels: map[string]string{
				"foo": "bar",
			}},
	}
	_, err = client.CoreV1().Namespaces().Create(context.TODO(), &ns2, metav1.CreateOptions{})
	require.NoError(t, err)

	defer func() {
		_ = client.CoreV1().Namespaces().Delete(context.TODO(), ns.Name, metav1.DeleteOptions{})
		_ = client.CoreV1().Namespaces().Delete(context.TODO(), ns2.Name, metav1.DeleteOptions{})
	}()

	roles := client.RbacV1().Roles(prefix + "test")

	const MaxWaitTime = 1000 * time.Millisecond
	t.Run("replicates from existing role", func(t *testing.T) {
		source := rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "source",
				Namespace: ns.Name,
				Annotations: map[string]string{
					common.ReplicationAllowed:           "true",
					common.ReplicationAllowedNamespaces: ns.Name,
				},
			},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"list", "get", "watch"},
			}},
		}

		target := rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "target",
				Namespace: ns.Name,
				Annotations: map[string]string{
					common.ReplicateFromAnnotation: common.MustGetKey(&source),
				},
			},
		}

		wg, stop := waitForRoles(client, 3, EventHandlerFuncs{
			AddFunc: func(wg *sync.WaitGroup, obj interface{}) {
				role := obj.(*rbacv1.Role)
				if role.Namespace == source.Namespace && role.Name == source.Name {
					log.Debugf("AddFunc %+v", obj)
					wg.Done()
				} else if role.Namespace == target.Namespace && role.Name == target.Name {
					log.Debugf("AddFunc %+v", obj)
					wg.Done()
				}
			},
			UpdateFunc: func(wg *sync.WaitGroup, oldObj interface{}, newObj interface{}) {
				role := oldObj.(*rbacv1.Role)
				if role.Namespace == target.Namespace && role.Name == target.Name {
					log.Debugf("UpdateFunc %+v -> %+v", oldObj, newObj)
					wg.Done()
				}
			},
		})

		_, err := roles.Create(context.TODO(), &source, metav1.CreateOptions{})
		require.NoError(t, err)

		_, err = roles.Create(context.TODO(), &target, metav1.CreateOptions{})
		require.NoError(t, err)

		waitWithTimeout(wg, MaxWaitTime)
		close(stop)

		updTarget, err := roles.Get(context.TODO(), target.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.EqualValues(t, source.Rules, updTarget.Rules)
	})

	t.Run("replication is pushed to other namespaces", func(t *testing.T) {
		source := rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "source-pushed-to-other-ns",
				Namespace: ns.Name,
				Annotations: map[string]string{
					common.ReplicateTo: prefix + "test2",
				},
			},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"list", "get", "watch"},
			}},
		}

		wg, stop := waitForRoles(client, 2, EventHandlerFuncs{
			AddFunc: func(wg *sync.WaitGroup, obj interface{}) {
				role := obj.(*rbacv1.Role)
				if role.Namespace == source.Namespace && role.Name == source.Name {
					log.Debugf("AddFunc %+v", obj)
					wg.Done()
				} else if role.Namespace == prefix+"test2" && role.Name == source.Name {
					log.Debugf("AddFunc %+v", obj)
					wg.Done()
				}
			},
		})
		_, err := roles.Create(context.TODO(), &source, metav1.CreateOptions{})
		require.NoError(t, err)

		waitWithTimeout(wg, MaxWaitTime)
		close(stop)

		roles2 := client.RbacV1().Roles(prefix + "test2")
		updTarget, err := roles2.Get(context.TODO(), source.Name, metav1.GetOptions{})

		require.NoError(t, err)
		require.EqualValues(t, source.Rules, updTarget.Rules)

		wg, stop = waitForRoles(client, 2, EventHandlerFuncs{
			UpdateFunc: func(wg *sync.WaitGroup, oldObj interface{}, newObj interface{}) {
				role := oldObj.(*rbacv1.Role)
				if role.Namespace == prefix+"test2" && role.Name == source.Name {
					log.Debugf("UpdateFunc %+v -> %+v", oldObj, newObj)
					wg.Done()
				}
			},
		})

		_, err = roles.Patch(context.TODO(), source.Name, types.JSONPatchType, []byte(`[{"op": "remove", "path": "/rules/0"}]`), metav1.PatchOptions{})
		require.NoError(t, err)

		waitWithTimeout(wg, MaxWaitTime)
		close(stop)

		updTarget, err = roles2.Get(context.TODO(), source.Name, metav1.GetOptions{})
		require.NoError(t, err)

		require.Len(t, updTarget.Rules, 0)
	})

	t.Run("roles are replicated when new namespace is created", func(t *testing.T) {
		namespaceName := prefix + "test-repl-new-ns"
		source := rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "source6",
				Namespace: ns.Name,
				Annotations: map[string]string{
					common.ReplicateTo: namespaceName,
				},
			},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"list", "get", "watch"},
			}},
		}

		wg, stop := waitForRoles(client, 1, EventHandlerFuncs{
			AddFunc: func(wg *sync.WaitGroup, obj interface{}) {
				role := obj.(*rbacv1.Role)
				if role.Namespace == source.Namespace && role.Name == source.Name {
					log.Debugf("AddFunc %+v", obj)
					wg.Done()
				}
			},
		})

		_, err := roles.Create(context.TODO(), &source, metav1.CreateOptions{})
		require.NoError(t, err)

		waitWithTimeout(wg, MaxWaitTime)
		close(stop)

		ns3 := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}}

		wg, stop = waitForNamespaces(client, 1, EventHandlerFuncs{
			AddFunc: func(wg *sync.WaitGroup, obj interface{}) {
				ns := obj.(*corev1.Namespace)
				if ns.Name == ns3.Name {
					log.Debugf("AddFunc %+v", obj)
					wg.Done()
				}
			},
		})

		wg2, stop2 := waitForRoles(client, 1, EventHandlerFuncs{
			AddFunc: func(wg *sync.WaitGroup, obj interface{}) {
				role := obj.(*rbacv1.Role)
				if role.Namespace == ns3.Name && role.Name == source.Name {
					log.Debugf("AddFunc %+v", obj)
					wg.Done()
				}
			},
		})

		_, err = client.CoreV1().Namespaces().Create(context.TODO(), &ns3, metav1.CreateOptions{})
		require.NoError(t, err)

		defer func() {
			_ = client.CoreV1().Namespaces().Delete(context.TODO(), ns3.Name, metav1.DeleteOptions{})
		}()

		waitWithTimeout(wg, MaxWaitTime)
		close(stop)

		waitWithTimeout(wg2, MaxWaitTime)
		close(stop2)

		roles3 := client.RbacV1().Roles(namespaceName)
		updTarget, err := roles3.Get(context.TODO(), source.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.EqualValues(t, source.Rules, updTarget.Rules)

		wg, stop = waitForRoles(client, 1, EventHandlerFuncs{
			UpdateFunc: func(wg *sync.WaitGroup, objOld interface{}, objNew interface{}) {
				role := objOld.(*rbacv1.Role)
				if role.Namespace == ns3.Name && role.Name == source.Name {
					log.Debugf("UpdateFunc %+v", objOld)
					wg.Done()
				}
			},
		})
		_, err = roles.Patch(context.TODO(), source.Name, types.JSONPatchType, []byte(`[{"op": "remove", "path": "/rules/0"}]`), metav1.PatchOptions{})
		require.NoError(t, err)

		waitWithTimeout(wg, MaxWaitTime)
		close(stop)

		updTarget, err = roles3.Get(context.TODO(), source.Name, metav1.GetOptions{})
		require.NoError(t, err)

		require.Len(t, updTarget.Rules, 0)
	})

}

func waitForNamespaces(client *kubernetes.Clientset, count int, eventHandlers EventHandlerFuncs) (wg *sync.WaitGroup, stop chan struct{}) {
	wg = &sync.WaitGroup{}
	wg.Add(count)
	informerFactory := informers.NewSharedInformerFactory(client, 60*time.Second)
	informer := informerFactory.Core().V1().Namespaces().Informer()
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if eventHandlers.AddFunc != nil {
				eventHandlers.AddFunc(wg, obj)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if eventHandlers.UpdateFunc != nil {
				eventHandlers.UpdateFunc(wg, oldObj, newObj)
			}

		},
		DeleteFunc: func(obj interface{}) {
			if eventHandlers.DeleteFunc != nil {
				eventHandlers.DeleteFunc(wg, obj)
			}
		},
	})
	stop = make(chan struct{})
	go informerFactory.Start(stop)

	return

}

func waitForRoles(client *kubernetes.Clientset, count int, eventHandlers EventHandlerFuncs) (wg *sync.WaitGroup, stop chan struct{}) {
	wg = &sync.WaitGroup{}
	wg.Add(count)
	informerFactory := informers.NewSharedInformerFactory(client, 60*time.Second)
	informer := informerFactory.Rbac().V1().Roles().Informer()
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if eventHandlers.AddFunc != nil {
				eventHandlers.AddFunc(wg, obj)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if eventHandlers.UpdateFunc != nil {
				eventHandlers.UpdateFunc(wg, oldObj, newObj)
			}

		},
		DeleteFunc: func(obj interface{}) {
			if eventHandlers.DeleteFunc != nil {
				eventHandlers.DeleteFunc(wg, obj)
			}
		},
	})
	stop = make(chan struct{})
	go informerFactory.Start(stop)

	return

}

func waitWithTimeout(wg *sync.WaitGroup, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-time.After(timeout):
		err := pkgerrors.Errorf("Timeout hit")
		log.WithError(err).Debugf("Wait timed out")
	}
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}
