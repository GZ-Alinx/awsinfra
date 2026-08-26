{{- define "jaeger-stack.name" -}}
{{- default .Chart.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "jaeger-stack.labels" -}}
app.kubernetes.io/name: {{ include "jaeger-stack.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
ops-deploy.io/project: {{ .Values.project | quote }}
ops-deploy.io/environment: {{ .Values.environment | quote }}
ops-deploy.io/stack: jaeger
{{- end -}}

{{- define "jaeger-stack.selector" -}}
app.kubernetes.io/name: {{ include "jaeger-stack.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
