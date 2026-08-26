{{- define "observability-otel.name" -}}
{{- default .Chart.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observability-otel.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: observability
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
ops-deploy.io/project: {{ .Values.project | quote }}
ops-deploy.io/environment: {{ .Values.environment | quote }}
{{- end -}}

{{- define "observability-otel.gatewayLabels" -}}
app.kubernetes.io/name: {{ include "observability-otel.name" . }}
app.kubernetes.io/component: gateway
{{- end -}}

{{- define "observability-otel.agentLabels" -}}
app.kubernetes.io/name: opentelemetry-agent
app.kubernetes.io/component: agent
{{- end -}}
