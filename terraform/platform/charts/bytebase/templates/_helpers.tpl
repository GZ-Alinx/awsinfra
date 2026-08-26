{{- define "bytebase.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "bytebase.labels" -}}
app.kubernetes.io/name: bytebase
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "bytebase.selectorLabels" -}}
app.kubernetes.io/name: bytebase
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
