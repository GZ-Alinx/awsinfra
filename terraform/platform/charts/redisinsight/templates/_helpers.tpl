{{- define "redisinsight.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "redisinsight.labels" -}}
app.kubernetes.io/name: redisinsight
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "redisinsight.selectorLabels" -}}
app.kubernetes.io/name: redisinsight
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
