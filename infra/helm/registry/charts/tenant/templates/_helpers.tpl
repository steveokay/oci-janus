{{- define "tenant.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "tenant.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "registry-%s" .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- define "tenant.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "tenant.labels" -}}
helm.sh/chart: {{ include "tenant.chart" . }}
{{ include "tenant.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "tenant.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tenant.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "tenant.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "tenant.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
{{- define "tenant.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}
