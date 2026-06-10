{{- define "storage.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "storage.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "registry-%s" .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- define "storage.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "storage.labels" -}}
helm.sh/chart: {{ include "storage.chart" . }}
{{ include "storage.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "storage.selectorLabels" -}}
app.kubernetes.io/name: {{ include "storage.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "storage.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "storage.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
{{- define "storage.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}
