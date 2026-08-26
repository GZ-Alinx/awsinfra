{{- define "rabbitmq.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "rabbitmq.labels" -}}
app.kubernetes.io/name: rabbitmq
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "rabbitmq.selectorLabels" -}}
app.kubernetes.io/name: rabbitmq
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
