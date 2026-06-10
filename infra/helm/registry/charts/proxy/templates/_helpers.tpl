{{- define "proxy.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "proxy.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "registry-%s" .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- define "proxy.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "proxy.labels" -}}
helm.sh/chart: {{ include "proxy.chart" . }}
{{ include "proxy.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "proxy.selectorLabels" -}}
app.kubernetes.io/name: {{ include "proxy.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "proxy.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "proxy.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
{{- define "proxy.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}
