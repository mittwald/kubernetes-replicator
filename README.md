# ConfigMap & Secret replication for Kubernetes

This repository contains a custom Kubernetes controller that can be used to make
to replicate secrets and config maps, in order to make them available in multiple namespaces or to avoid for them to be updated on chart deployments.

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

### 1. Allow a source secret to be replicated from

If a secret or configMap needs to be replicated to other namespaces, annotations should be added in that object permitting replication.

  - Add `v1.kubernetes-replicator.olli.com/replication-allowed` annotation with value `true` indicating that the object can be replicated.
  - Add `v1.kubernetes-replicator.olli.com/replication-allowed-namespaces` annotation. Value of this annotation should contain a comma separated list of permitted namespaces or regular expressions. For example `namespace-1,my-ns-2,app-ns-[0-9]*`: in this case replication will be performed only into the namespaces `namespace-1` and `my-ns-2` as well as any namespace that matches the regular expression `app-ns-[0-9]*`.
  - You can add `v1.kubernetes-replicator.olli.com/replicate-once` annotation to ensure that the secret will only be replicated once, no matter how many times it is updated.
  - You can add `v1.kubernetes-replicator.olli.com/replicate-once-version` annotation to still replicate the secret when the once version is updated.

    ```yaml
    apiVersion: v1
    kind: Secret
    metadata:
      annotations:
        v1.kubernetes-replicator.olli.com/replication-allowed: "true"
        v1.kubernetes-replicator.olli.com/replication-allowed-namespaces: "my-ns-1,namespace-[0-9]*"
        # v1.kubernetes-replicator.olli.com/replicate-once: "true"
        # v1.kubernetes-replicator.olli.com/replicate-once-version: "0.0.1"
    data:
      key1: <value>
    ```

### 2. Create an empty secret to replicate into

To make the secret available in other namespaces, create an empty secret to be replicated into.

  - Add the annotation `v1.kubernetes-replicator.olli.com/replicate-from` to any Kubernetes secret or config map object. The value of that annotation should contain the the name of another secret or config map (using `<name>` or `<namespace>/<name>` notation).
  - You can add `v1.kubernetes-replicator.olli.com/replicate-once` annotation to ensure that the secret will only be replicated once, no matter how many times it is updated.

    ```yaml
    apiVersion: v1
    kind: Secret
    metadata:
      annotations:
        v1.kubernetes-replicator.olli.com/replicate-from: default/some-secret
        # v1.kubernetes-replicator.olli.com/replicate-once: "true"
    data: {}
    ```

The replicator will then copy the `data` attribute of the referenced object into the annotated object and keep them in sync.

### 3. Create a source secret to copy from

If a secret needs to have a copy, annotation should be added in that object to trigger replication.

  - Add `v1.kubernetes-replicator.olli.com/replicate-to` to any Kubernetes secret or config map object. The value of that annotation should contain the the name of another secret or config map (using `<name>` or `<namespace>/<name>` notation).
  - You can add `v1.kubernetes-replicator.olli.com/replicate-once` annotation to ensure that the secret will only be replicated once, no matter how many times it is updated.
  - You can add `v1.kubernetes-replicator.olli.com/replicate-once-version` annotation to still replicate the secret when the once version is updated.

    ```yaml
    apiVersion: v1
    kind: Secret
    metadata:
      annotations:
        v1.kubernetes-replicator.olli.com/replicate-to: default/some-other-secret
        v1.kubernetes-replicator.olli.com/replicate-once: "true"
        # v1.kubernetes-replicator.olli.com/replicate-once-version: "0.0.1"
    data:
      key1: <value>
    ```
The secret will then create another secret with the a copy of the `data` attribute. Combined with once annotation, this can be used to freeze a secret whose content is randomly generated and updated by an Helm chart.
