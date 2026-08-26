{{- define "otel-elasticsearch.name" -}}
{{- default "otel-elasticsearch" .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "otel-elasticsearch.labels" -}}
app.kubernetes.io/name: {{ include "otel-elasticsearch.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: opentelemetry
ops-deploy.io/stack: opentelemetry
ops-deploy.io/component: otel-elasticsearch
ops-deploy.io/project: {{ .Values.project | quote }}
ops-deploy.io/environment: {{ .Values.environment | quote }}
{{- end }}

{{- define "otel-elasticsearch.selector" -}}
app.kubernetes.io/name: {{ include "otel-elasticsearch.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
