# ConfigMap & Secret replication for Kubernetes

This repository contains a custom Kubernetes controller that can be used to replicate secrets and config maps, in order to make them available in multiple namespaces or to avoid for them to be updated on chart deployments.

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

### Receiving a copy of secret or configMap

You can configure a secret or a configMap to receive a copy of another secret or configMap:

    ```yaml
    apiVersion: v1
    kind: ConfigMap
    metadata:
      annotations:
        v1.kubernetes-replicator.olli.com/replicate-from: default/some-secret
    data: {}
    ```

Annotations are:
  - `v1.kubernetes-replicator.olli.com/replicate-from`: The source of the data to receive a copy from. Can be a full path `<namespace>/<name>`, or just a name if the source is in the same namespace.
  - `v1.kubernetes-replicator.olli.com/replicate-once`: Set it to `"true"` for being replicated only once, no matter to the future changes of the source. Can be useful if the source is a randomly generated password, but you don't want your local passowrd to change anymore.

Unless you run kubernetes-replicator with the `--allow-all` flag, you need to explicitely allow the source to be replicated:

    ```yaml
    apiVersion: v1
    kind: ConfigMap
    metadata:
      annotations:
        v1.kubernetes-replicator.olli.com/replication-allowed: "true"
    data: {}
    ```

At leat one of the two annotations is required (if the `--allow-all` is not used):
  - `v1.kubernetes-replicator.olli.com/replication-allowed`: Set it to `"true"` to explicitely allow replication, or `"false"` to explicitely diswallow it
  - `v1.kubernetes-replicator.olli.com/replication-allowed-namespaces`: a comma separated list of namespaces or namespaces patterns to explicitely allow. ex: `"my-namespace,test-namespace-[0-9]+"`

Other annotations are:
  - `v1.kubernetes-replicator.olli.com/replicate-once`: Set it to `"true"` for being replicated only once, no matter future changes. Can be useful if the secret is a randomly generated password, but you don't want the local copies to change anymore.
  - `v1.kubernetes-replicator.olli.com/replicate-once-version`: A semver2 version. When a higher version is set, this secret or confingMap is replicated again, even if replicated once. It allows a thinner control on the `v1.kubernetes-replicator.olli.com/replicate-once` annotation. If absent, version is assumed to be `"0.0.0"`. `"5"` will be interpreted as `"5.0.0"`.

The content of the target secret of configMap will be emptied if the source does nto exist or is deleted.

### Replicating a secret or configMap to other locations

You can configure a secret or a configMap to replicate itself automatically to desired locations:

    ```yaml
    apiVersion: v1
    kind: ConfigMap
    metadata:
      annotations:
        v1.kubernetes-replicator.olli.com/replicate-to: default/other-secret
    data: {}
    ```

At leat one of the two annotations is required:
  - `v1.kubernetes-replicator.olli.com/replicate-to`: The target(s) of the annotation, comma separated. Can be a name, a full path `<namespace>/<name>`, or a pattern `<namesapce_pattern>/<name>`. If just given a name, it will be combined with the namespace of the source, or with the `v1.kubernetes-replicator.olli.com/replicate-to-namespaces` annotation if present. ex: `"other-secret,other-namespace/another-secret,test-namespace-[0-9]+/nyan-secret"`
  - `v1.kubernetes-replicator.olli.com/replicate-to-namespaces`: The target namespace(s) for replication, comma separated. it will be combined with the name of the source, or with the `v1.kubernetes-replicator.olli.com/replicate-to` if present. ex: `"other-namespace,test-namespace-[0-9]+"`

Other annotations are:
  - `v1.kubernetes-replicator.olli.com/replicate-once`: Set it to `"true"` for being replicated only once, no matter future changes. Can be useful if the secret is a randomly generated password, but you don't want the local copies to change anymore.
  - `v1.kubernetes-replicator.olli.com/replicate-once-version`: A semver2 version. When a higher version is set, this secret or confingMap is replicated again, even if replicated once. It allows a thinner control on the `v1.kubernetes-replicator.olli.com/replicate-once` annotation. If absent, version is assumed to be `"0.0.0"`. `"5"` will be interpreted as `"5.0.0"`.

Replication will be cancelled if the target secret or configMap already exists but was not created by replication from this source. However, as soon as that existing target is deleted, it will be replaced by a replication of the source.

Once the source secret or configMap is deleted or its annotations are changed, the target is deleted.

## Examples

### Import database credentials anywhere

Create the source secret

    ```yaml
    apiVersion: v1
    kind: Secret
    type: Opaque
    metadata:
      name: database-credentials
      namespace: default
      annotations:
        v1.kubernetes-replicator.olli.com/replication-allowed: "true"
    stringData:
      host: mydb.com
      database: mydb
      password: qwerty
    ```

You can now create an empty secret everywhere you needs this (including in helm charts)

    ```yaml
    apiVersion: v1
    kind: Secret
    type: Opaque
    metadata:
      name: local-database-credentials
      annotations:
        v1.kubernetes-replicator.olli.com/replicate-from: "default/database-credentials"
    ```

### Use random password generated by an helm chart

Create your source secret with a random password, and replicate it once

    ```yaml
    apiVersion: v1
    kind: Secret
    type: Opaque
    metadata:
      name: admin-password-source
      annotations:
        v1.kubernetes-replicator.olli.com/replicate-to: "admin-password"
        v1.kubernetes-replicator.olli.com/replicate-once: "true"
        # in case of the secret format changes
        v1.kubernetes-replicator.olli.com/replicate-once-version: "0"
    stringData:
      password: {{ randAlphaNum 64 | quote }}
    ```

And use it in your deployment

    ```yaml
    apiVersion: extensions/v1beta1
    kind: Deployment
    spec:
      template:
        spec:
          containers:
          - name: my-container
            image: gcr.io/my-project/my-container:latest
            env:
            - name: ADMIN_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: admin-password
                  key: password
    ```

### Spread your TLS key

Create your TLS secret

    ```yaml
    apiVersion: v1
    kind: Secret
    type: kubernetes.io/tls
    metadata:
      name: my-tls
      namespace: jx
      annotations:
        v1.kubernetes-replicator.olli.com/replication-to-namespaces: "jx-.*"
    stringData:
      tls.crt: |
        -----BEGIN CERTIFICATE-----
        [...]
        -----END CERTIFICATE-----
      tls.key: |
        -----BEGIN RSA PRIVATE KEY-----
        [...]
        -----END RSA PRIVATE KEY-----
    ```

And use it in your ingresses

    ```yaml
    apiVersion: networking.k8s.io/v1beta1
    kind: Ingress
    spec:
      tls:
      - hosts:
        - example.com
        secretName: my-tls
    ```
