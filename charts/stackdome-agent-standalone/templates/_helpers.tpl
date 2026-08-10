{{/*
Chart name.
*/}}
{{- define "stackdome-agent-standalone.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Privileged registry config reconciler image reference.
*/}}
{{- define "stackdome-agent-standalone.registryConfigReconcilerImage" -}}
{{- if or (not (regexMatch "^[^@[:space:]]+$" .Values.registryConfigReconciler.repository)) (regexMatch "/[^/]+:" .Values.registryConfigReconciler.repository) }}
{{- fail "registryConfigReconciler.repository must be an untagged image repository" }}
{{- end }}
{{- if .Values.registryConfigReconciler.digest }}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" .Values.registryConfigReconciler.digest) }}
{{- fail "registryConfigReconciler.digest must be sha256:<64 lowercase hex characters>" }}
{{- end }}
{{- printf "%s@%s" .Values.registryConfigReconciler.repository .Values.registryConfigReconciler.digest }}
{{- else }}
{{- if .Values.registryConfigReconciler.requireDigest }}
{{- fail "registryConfigReconciler.digest is required when registryConfigReconciler.requireDigest=true" }}
{{- end }}
{{- $tag := default .Chart.AppVersion .Values.registryConfigReconciler.tag }}
{{- printf "%s:%s" .Values.registryConfigReconciler.repository $tag }}
{{- end }}
{{- end }}

{{/*
Fully qualified app name. Truncated to 63 chars because some Kubernetes name fields are limited.
*/}}
{{- define "stackdome-agent-standalone.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if or (contains $name .Release.Name) (contains .Release.Name $name) }}
{{- $name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label.
*/}}
{{- define "stackdome-agent-standalone.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "stackdome-agent-standalone.labels" -}}
helm.sh/chart: {{ include "stackdome-agent-standalone.chart" . }}
{{ include "stackdome-agent-standalone.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "stackdome-agent-standalone.selectorLabels" -}}
app.kubernetes.io/name: {{ include "stackdome-agent-standalone.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name.
*/}}
{{- define "stackdome-agent-standalone.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "stackdome-agent-standalone.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image reference.
*/}}
{{- define "stackdome-agent-standalone.image" -}}
{{- if .Values.image.digest }}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else }}
{{- $tag := default .Chart.AppVersion .Values.image.tag }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}
{{- end }}
