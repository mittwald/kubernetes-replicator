# ConfigMap & Secret replication for Kubernetes

[![Build Status](https://travis-ci.org/mittwald/kubernetes-replicator.svg?branch=master)](https://travis-ci.org/mittwald/kubernetes-replicator)

This repository contains a custom Kubernetes controller that can be used to make
secrets and config maps available in multiple namespaces.

## Contents

1. [Deployment](#deployment)
    1. [Using Helm](#using-helm)
    1. [Manual](#manual)
1. [Usage](#usage)
    1. [1. Create the source secret](#1-create-the-source-secret)
    1. [2. Create empty secret](#2-create-empty-secret)
    1. [Special case: TLS secrets](#special-case-tls-secrets)

## Deployment

### Using Helm

```shellsession
$ helm upgrade --install kubernetes-replicator ./deploy/helm-chart/kubernetes-replicator
```

### Manual

```shellsession
$ # Create roles and service accounts
$ kubectl apply -f https://raw.githubusercontent.com/mittwald/kubernetes-replicator/master/deploy/rbac.yaml
$ # Create actual deployment
$ kubectl apply -f https://raw.githubusercontent.com/mittwald/kubernetes-replicator/master/deploy/deployment.yaml
```

## Usage

### 1. Create the source secret

If a secret or configMap needs to be replicated to other namespaces, annotations should be added in that object permitting replication.
 
  - Add `replicator.v1.mittwald.de/replication-allowed` annotation with value `true` indicating that the object can be replicated.
  - Add `replicator.v1.mittwald.de/replication-allowed-namespaces` annotation. Value of this annotation should contain a comma separated list of permitted namespaces or regular expressions. For example `namespace-1,my-ns-2,app-ns-[0-9]*`: in this case replication will be performed only into the namespaces `namespace-1` and `my-ns-2` as well as any namespace that matches the regular expression `app-ns-[0-9]*`.

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

### 2. Create empty secret

Add the annotation `replicator.v1.mittwald.de/replicate-from` to any Kubernetes secret or config map object. The value of that annotation should contain the the name of another secret or config map (using `<namespace>/<name>` notation).

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: secret-replica
  annotations:
    replicator.v1.mittwald.de/replicate-from: default/some-secret
data: {}
```

The replicator will then copy the `data` attribute of the referenced object into the annotated object and keep them in sync.   

### Special case: TLS secrets

Secrets of type `kubernetes.io/tls` are treated in a special way and need to have a `data["tls.crt"]` and a `data["tls.key"]` property to begin with. In the replicated secrets, these properties need to be present to begin with, but they may be empty:

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

### Special case: Docker registry credentials

Secrets of type `kubernetes.io/dockerconfigjson` also require special treatment. These secrets require to have a `.dockerconfigjson` key that needs to require valid JSON. For this reason, a replicated secret of this type should be created as follows:

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