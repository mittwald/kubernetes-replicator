# ConfigMap & Secret replication for Kubernetes

[![Docker Repository on Quay](https://quay.io/repository/mittwald/kubernetes-replicator/status "Docker Repository on Quay")](https://quay.io/repository/mittwald/kubernetes-replicator)
[![Build Status](https://travis-ci.org/mittwald/kubernetes-replicator.svg?branch=master)](https://travis-ci.org/mittwald/kubernetes-replicator)

This repository contains a custom Kubernetes controller that can be used to make
secrets and config maps available in multiple namespaces.

## Deployment

```shellsession
$ # Create roles and service accounts
$ kubectl apply -f https://raw.githubusercontent.com/mittwald/kubernetes-replicator/master/deploy/rbac.yaml
$ # Create actual deployment
$ kubectl apply -f https://raw.githubusercontent.com/mittwald/kubernetes-replicator/master/deploy/deployment.yaml
```

## Usage

Add the annotation `replicator.v1.mittwald.de/replicate-from` to any Kubernetes
secret or config map object. The value of that annotation should contain the
the name of another secret or config map (using `<namespace>/<name>` notation).

```yaml
apiVersion: v1
kind: Secret
metadata:
  annotations:
    replicator.v1.mittwald.de/replicate-from: default/some-secret
data: {}
```

The replicator will then copy the `data` attribute of the referenced object into
the annotated object and keep them in sync.   
