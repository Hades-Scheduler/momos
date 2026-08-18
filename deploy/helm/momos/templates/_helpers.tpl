{{- define "momos.name" -}}
{{- default "momos" .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "momos.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "momos.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "momos.labels" -}}
app.kubernetes.io/name: {{ include "momos.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "momos.selectorLabels" -}}
app.kubernetes.io/name: {{ include "momos.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "momos.secretName" -}}
{{- if .Values.existingSecret -}}{{ .Values.existingSecret }}{{- else -}}{{ include "momos.fullname" . }}{{- end -}}
{{- end -}}
