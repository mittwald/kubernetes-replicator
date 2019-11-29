{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "kubernetes-replicator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "kubernetes-replicator.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "kubernetes-replicator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kubernetes-replicator.prefix" -}}
  {{- $test := and (eq .Release.Name "testRelease") (eq .Release.Namespace "default") -}}
  {{- if .Values.spanner.prefix -}}
  	{{ .Values.spanner.prefix | quote }}
  {{- else if hasPrefix .Values.previewNSPrefix .Release.Namespace -}}
    "{{ .Release.Namespace | replace .Values.spanner.previewNSPrefix "" | trimPrefix "-" }}.preview.kubernetes-replicator.olli.com/""
  {{- else if eq .Release.Namespace "jx" -}}
  	"v1.kubernetes-replicator.olli.com/"
  {{- else if $test -}}
  	"noprefix/"
  {{- else -}}
    {{- printf "[release %s] no known prefix for namespace \"%s\", expected \"jx\" or \"%s-pr-n\"" .Release.Name .Release.Namespace .Values.previewNSPrefix | fail -}}
  {{- end -}}
{{- end -}}
