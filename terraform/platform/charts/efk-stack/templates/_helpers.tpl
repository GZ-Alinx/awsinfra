{{- define "efk-stack.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: efk-stack
ops-deploy.io/stack: efk
ops-deploy.io/project: {{ .Values.project | quote }}
ops-deploy.io/environment: {{ .Values.environment | quote }}
{{- end }}

{{- define "efk-stack.selector" -}}
app.kubernetes.io/name: {{ . }}
app.kubernetes.io/part-of: efk-stack
{{- end }}

{{- define "efk-stack.regex" -}}
{{- $parts := list -}}
{{- range . -}}
{{- $parts = append $parts (regexQuoteMeta .) -}}
{{- end -}}
{{- join "|" $parts -}}
{{- end }}

{{- define "efk-stack.indexPrefix" -}}
{{- printf "kubernetes-%s-%s" .Values.project .Values.environment | lower | replace "_" "-" | trunc 120 | trimSuffix "-" -}}
{{- end }}

{{- define "efk-stack.bootstrapHash" -}}
{{- printf "%s|%s|%s|%v|%v|%v|%v|%v" .Chart.Version .Values.images.elasticsearch .Values.images.kibana .Values.elasticsearch.retentionDays .Values.collection.includeNamespaces .Values.collection.excludeNamespaces .Values.collection.includeServices .Values.collection.excludeServices | sha256sum | trunc 10 -}}
{{- end }}
