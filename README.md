# ConfigMap, Secret and Role, and RoleBinding replication for Kubernetes

![Build Status](https://github.com/mittwald/kubernetes-replicator/workflows/Compile%20&%20Test/badge.svg)

This repository contains a custom Kubernetes controller that can be used to make
secrets and config maps available in multiple namespaces.

## Contents

1. [Deployment](#deployment)
    1. [Using Helm](#using-helm)
    1. [Manual](#manual)
1. [Usage](#usage)
    1. ["Role and RoleBinding replication](#role-and-rolebinding-replication)
    1. ["Push-based" replication](#push-based-replication)
    1. ["Pull-based" replication](#pull-based-replication)
        1. [1. Create the source secret](#step-1-create-the-source-secret)
        1. [2. Create empty secret](#step-2-create-an-empty-destination-secret)
        1. [Special case: TLS secrets](#special-case-tls-secrets)

## Deployment

### Using Helm

1. Add the Mittwald Helm Repo:
    ```shellsession
    $ helm repo add mittwald https://helm.mittwald.de
    "mittwald" has been added to your repositories

    $ helm repo update
    Hang tight while we grab the latest from your chart repositories...
    ...Successfully got an update from the "mittwald" chart repository
    Update Complete. ⎈ Happy Helming!⎈
    ```

2. Upgrade or install `kubernetes-replicator`
    `helm upgrade --install kubernetes-replicator mittwald/kubernetes-replicator`

### Manual

```shellsession
$ # Create roles and service accounts
$ kubectl apply -f https://raw.githubusercontent.com/mittwald/kubernetes-replicator/master/deploy/rbac.yaml
$ # Create actual deployment
$ kubectl apply -f https://raw.githubusercontent.com/mittwald/kubernetes-replicator/master/deploy/deployment.yaml
```

## Usage

### Role and RoleBinding replication

To create a new role, your own account needs to have at least the same set of privileges as the role you're trying to create. The chart currently offers two options to grant these permissions to the service account used by the replicator:

- Set the value `grantClusterAdmin`to `true`, which grants the service account admin privileges. This is set to `false` by default, as having a service account with that level of access might be undesirable due to the potential security risks attached. 

- Set the lists of needed api groups and resources explicitely. These can be specified using the value `privileges`. `privileges` is a list that contains pairs of api group and resource lists. 
  
  Example:

  ```yaml
  serviceAccount:
    create: true
    annotations: {}
    name:
    privileges:
      - apiGroups: [ "", "apps", "extensions" ] 
        resources: ["secrets", "configmaps", "roles", "rolebindings",
        "cronjobs", "deployments", "events", "ingresses", "jobs", "pods", "pods/attach", "pods/exec", "pods/log", "pods/portforward", "services"]
      - apiGroups: [ "batch" ]
        resources:  ["configmaps", "cronjobs", "deployments", "events", "ingresses", "jobs", "pods", "pods/attach", "pods/exec", "pods/log", "pods/portforward", "services"]
  ```

  These settings permit the replication of Roles and RoleBindings with privileges for the api groups `""`. `apps`, `batch` and `extensions` on the resources specified. 

### "Push-based" replication

Push-based replication will "push out" the secrets, configmaps, roles and rolebindings into namespaces when new namespaces are created or when the secret/configmap/roles/rolebindings changes.

There are two general methods for push-based replication:

- name-based; this allows you to either specify your target namespaces _by name_ or by regular expression (which should match the namespace name). To use name-based push replication, add a `replicator.v1.mittwald.de/replicate-to` annotation to your secret, role(binding) or configmap. The value of this annotation should contain a comma separated list of permitted namespaces or regular expressions. (Example: `namespace-1,my-ns-2,app-ns-[0-9]*` will replicate only into the namespaces `namespace-1` and `my-ns-2` as well as any namespace that matches the regular expression `app-ns-[0-9]*`).

  Example:

  ```yaml
  apiVersion: v1
  kind: Secret
  metadata:
    annotations:
      replicator.v1.mittwald.de/replicate-to: "my-ns-1,namespace-[0-9]*"
  data:
    key1: <value>
  ```

- label-based; this allows you to specify a label selector that a namespace should match in order for a secret, role(binding) or configmap to be replicated. To use label-based push replication, add a `replicator.v1.mittwald.de/replicate-to-matching` annotation to the object you want to replicate. The value of this annotation should contain an arbitrary [label selector](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors).

  Example:

  ```yaml
  apiVersion: v1
  kind: Secret
  metadata:
    annotations:
      replicator.v1.mittwald.de/replicate-to-matching: >
        my-label=value,my-other-label,my-other-label notin (foo,bar)
  data:
    key1: <value>
  ```

When the labels of a namespace are changed, any resources that were replicated by labels into the namespace and no longer qualify for replication under the new set of labels will be deleted. Afterwards any resources that now match the updated labels will be replicated into the namespace.

It is possible to use both methods of push-based replication together in a single resource, by specifying both annotations.

### "Pull-based" replication

Pull-based replication makes it possible to create a secret/configmap/role/rolebindings and select a "source" resource 
from which the data is replicated from.

#### Step 1: Create the source secret

If a secret or configMap needs to be replicated to other namespaces, annotations should be added in that object 
permitting replication.
 
  - Add `replicator.v1.mittwald.de/replication-allowed` annotation with value `true` indicating that the object can be 
    replicated.
  - Add `replicator.v1.mittwald.de/replication-allowed-namespaces` annotation. Value of this annotation should contain 
    a comma separated list of permitted namespaces or regular expressions. For example `namespace-1,my-ns-2,app-ns-[0-9]*`: 
    in this case replication will be performed only into the namespaces `namespace-1` and `my-ns-2` as well as any 
    namespace that matches the regular expression `app-ns-[0-9]*`.

    ```yaml
    apiVersion: v1
    kind: Secret
    metadata:
      annotations:
        replicator.v1.mittwald.de/replication-allowed: "true"
        replicator.v1.mittwald.de/replication-allowed-namespaces: "my-ns-1,namespace-[0-9]*"
    data:
      key1: <value>
    ```

#### Step 2: Create an empty destination secret

Add the annotation `replicator.v1.mittwald.de/replicate-from` to any Kubernetes secret or config map object. The value 
of that annotation should contain the the name of another secret or config map (using `<namespace>/<name>` notation).

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: secret-replica
  annotations:
    replicator.v1.mittwald.de/replicate-from: default/some-secret
data: {}
```

The replicator will then copy the `data` attribute of the referenced object into the annotated object and keep them in 
sync.   

#### Special case: TLS secrets

Secrets of type `kubernetes.io/tls` are treated in a special way and need to have a `data["tls.crt"]` and a 
`data["tls.key"]` property to begin with. In the replicated secrets, these properties need to be present to begin with, 
but they may be empty:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: tls-secret-replica
  annotations:
    replicator.v1.mittwald.de/replicate-from: default/some-tls-secret
type: kubernetes.io/tls
data:
  tls.key: ""
  tls.crt: ""
```

#### Special case: Docker registry credentials

Secrets of type `kubernetes.io/dockerconfigjson` also require special treatment. These secrets require to have a 
`.dockerconfigjson` key that needs to require valid JSON. For this reason, a replicated secret of this type should be 
created as follows:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: docker-secret-replica
  annotations:
    replicator.v1.mittwald.de/replicate-from: default/some-docker-secret
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: e30K
```

#### Special case: Resource with .metadata.ownerReferences

Sometimes, secrets are generated by external components. Such secrets are configured with an ownerReference. By default, the kubernetes-replicator will delete the 
ownerReference in the target namespace.

ownerReference won't work [across different namespaces](https://kubernetes.io/docs/concepts/workloads/controllers/garbage-collection/#owners-and-dependents) and the secret at the destination will be removed by the kubernetes garbage collection.

To keep `ownerReferences` at the destination, set the annotation `replicator.v1.mittwald.de/keep-owner-references=true`

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: docker-secret-replica
  annotations:
    replicator.v1.mittwald.de/keep-owner-references: "true"
  ownerReferences:
    - apiVersion: v1
      kind: Deployment
      name: owner
      uid: "1234"
type: kubernetes.io/tls
data:
  tls.key: ""
  tls.crt: ""
```

See also: https://github.com/mittwald/kubernetes-replicator/issues/120
