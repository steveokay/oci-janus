{{- define "signer.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "signer.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "registry-%s" .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- define "signer.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "signer.labels" -}}
helm.sh/chart: {{ include "signer.chart" . }}
{{ include "signer.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "signer.selectorLabels" -}}
app.kubernetes.io/name: {{ include "signer.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "signer.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "signer.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
{{- define "signer.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}
