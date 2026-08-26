{{- define "data-service.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "data-service.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "data-service.labels" -}}
app.kubernetes.io/name: {{ include "data-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: {{ .Values.engine }}
{{- end -}}

{{- define "data-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "data-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
