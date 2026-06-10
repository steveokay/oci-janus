{{- define "webhook.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "webhook.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "registry-%s" .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- define "webhook.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "webhook.labels" -}}
helm.sh/chart: {{ include "webhook.chart" . }}
{{ include "webhook.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "webhook.selectorLabels" -}}
app.kubernetes.io/name: {{ include "webhook.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "webhook.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "webhook.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
{{- define "webhook.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}
