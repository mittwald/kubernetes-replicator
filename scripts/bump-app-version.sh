#!/usr/bin/env bash

set -e

## make this script a bit more re-usable
GIT_REPOSITORY="github.com/mittwald/kubernetes-replicator.git"
CHART_YAML="./deploy/helm-chart/kubernetes-replicator/Chart.yaml"

## avoid noisy shellcheck warnings
TRAVIS_TAG="${TRAVIS_TAG:-v0.0.0}"
GITHUB_TOKEN="${GITHUB_TOKEN:-dummy}"

## Replace app-version
sed -i "s#appVersion:.*#appVersion: ${TRAVIS_TAG}#g" "${CHART_YAML}"

## Useful for debugging purposes
git status

## Set up Git-User
git config --global user.name "Mittwald Machine"
git config --global user.email "opensource@mittwald.de"

## Add new remote with credentials baked in url
git remote add publisher "https://mittwald-machine:${GITHUB_TOKEN}@${GIT_REPOSITORY}"

## add and commit changed file
git add "${CHART_YAML}"

## useful for debugging purposes
git status

## stage changes
git commit -m "Bump appVersion to '${TRAVIS_TAG}'"

if [[ "${1}" == "publish" ]]; then

    ## publish changes
    git pull --rebase publisher master
    git push publisher master

    ## trigger helm-chart reload
    curl -X POST 'https://api.github.com/repos/mittwald/helm-charts/dispatches' -u "mittwald-machine:${GITHUB_TOKEN}" -d '{"event_type": "updateCharts"}'

fi


exit 0