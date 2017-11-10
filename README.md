# Automatically generated secrets for Kubernetes

This repository contains a custom Kubernetes controller that can automatically create
random secret values. This may be used for auto-generating random credentials for
applications run on Kubernetes.

## Deployment

```shellsession
$ # Create roles and service accounts
$ kubectl apply -f https://raw.githubusercontent.com/mittwald/kubernetes-secret-generator/master/deploy/secret-generator-rbac.yaml
$ # Create actual deployment
$ kubectl apply -f https://raw.githubusercontent.com/mittwald/kubernetes-secret-generator/master/deploy/secret-generator.yaml
```

## Usage

Add the annotation `secret-generator.v1.mittwald.de/autogenerate` to any Kubernetes
secret object. The value of the annotation can be a field name within the secret; the
SecretGeneratorController will pick up this annotation and add a field (`password` in
the example below) to the secret with a randomly generated string value.

```yaml
apiVersion: v1
kind: Secret
metadata:
  annotations:
    secret-generator.v1.mittwald.de/autogenerate: password
data:
  username: c29tZXVzZXI=
```

## Operational tasks

-   Regenerate all automatically generated passwords:

    ```
    $ kubectl annotate secrets --all secret-generator.v1.mittwald.de/regenerate=true