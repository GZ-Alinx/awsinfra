{{- define "etcd-workbench.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "etcd-workbench.labels" -}}
app.kubernetes.io/name: etcd-workbench
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "etcd-workbench.selectorLabels" -}}
app.kubernetes.io/name: etcd-workbench
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
