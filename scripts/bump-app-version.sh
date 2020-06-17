#!/usr/bin/env bash

set -e

## debug if desired
if [[ -n "${DEBUG}" ]]; then
    set -x
fi

## make this script a bit more re-usable
GIT_REPOSITORY="github.com/mittwald/kubernetes-replicator.git"
CHART_YAML="./deploy/helm-chart/kubernetes-replicator/Chart.yaml"

## avoid noisy shellcheck warnings
GIT_TAG_NAME="${GIT_TAG_NAME:-v0.0.0}"
GITHUB_TOKEN="${GITHUB_TOKEN:-dummy}"

## temp working vars
TIMESTAMP="$(date +%s )"
TMP_DIR="/tmp/${TIMESTAMP}"

## set up Git-User
git config --global user.name "${RELEASE_USER_NAME}"
git config --global user.email "${RELEASE_USER_EMAIL}"

## temporary clone git repository
git clone "https://${GIT_REPOSITORY}" "${TMP_DIR}"
cd "${TMP_DIR}"

## replace appVersion
sed -i "s#appVersion:.*#appVersion: ${GIT_TAG_NAME}#g" "${CHART_YAML}"

## replace chart version with current tag without 'v'-prefix
sed -i "s#version:.*#version: ${GIT_TAG_NAME/v/}#g" "${CHART_YAML}"

## useful for debugging purposes
git status

## Add new remote with credentials baked in url
git remote add publisher "https://mittwald-machine:${GITHUB_TOKEN}@${GIT_REPOSITORY}"

## add and commit changed file
git add -A

## useful for debugging purposes
git status

## stage changes
git commit -m "Bump appVersion to '${GIT_TAG_NAME}'"

## rebase
git pull --rebase publisher master

if [[ "${1}" == "publish" ]]; then

    ## publish changes
    git push publisher master

    ## trigger helm-charts reload
    curl -X POST 'https://api.github.com/repos/mittwald/helm-charts/dispatches' -u "${RELEASE_USER}:${GITHUB_TOKEN}" -d '{"event_type": "updateCharts"}'

fi


exit 0