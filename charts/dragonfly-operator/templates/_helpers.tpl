{{/*
Expand the name of the chart.
*/}}
{{- define "dragonfly-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "dragonfly-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "dragonfly-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "dragonfly-operator.labels" -}}
helm.sh/chart: {{ include "dragonfly-operator.chart" . }}
{{ include "dragonfly-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/created-by: {{ include "dragonfly-operator.name" . }}
app.kubernetes.io/part-of: {{ include "dragonfly-operator.name" . }}
{{- if .Values.additionalLabels }}
{{ toYaml .Values.additionalLabels }}
{{- end }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "dragonfly-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dragonfly-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}

{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "dragonfly-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "dragonfly-operator.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Limit controller service name to 63 characters by truncating fullname at 48 characters to comply with DNS naming spec
Suffix + 48 char fullname = max 63 characters
*/}}
{{- define "dragonfly-operator.controllerServiceName" -}}
{{- printf "%s-controller-svc" (include "dragonfly-operator.fullname" . | trunc 48 | trimSuffix "-" ) -}}
{{- end -}}

{{/*
Allow the release namespace to be overridden for multi-namespace deployments in combined charts
*/}}
{{- define "dragonfly-operator.namespace" -}}
{{- if .Values.namespaceOverride -}}
{{- .Values.namespaceOverride -}}
{{- else -}}
{{- .Release.Namespace -}}
{{- end -}}
{{- end -}}
