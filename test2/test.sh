#!/bin/bash

kubectl create namespace test

kubectl apply -f secret1.yaml -n test
kubectl apply -f secret2.yaml -n test
kubectl apply -f secret3.yaml -n test
