{{- define "clickvisual-stack.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
ops-deploy.io/stack: clickvisual
ops-deploy.io/project: {{ .Values.project | quote }}
ops-deploy.io/environment: {{ .Values.environment | quote }}
{{- end }}

{{- define "clickvisual-stack.selector" -}}
ops-deploy.io/stack: clickvisual
ops-deploy.io/component: {{ . | quote }}
{{- end }}

{{- define "clickvisual-stack.kafkaClaim" -}}
{{- $root := index . 0 -}}
{{- $index := index . 1 -}}
{{- $claims := $root.Values.kafka.storage.activeClaims | default (list) -}}
{{- if gt (len $claims) $index -}}
{{- index $claims $index -}}
{{- else -}}
{{- printf "clickvisual-kafka-data-%d" $index -}}
{{- end -}}
{{- end }}
