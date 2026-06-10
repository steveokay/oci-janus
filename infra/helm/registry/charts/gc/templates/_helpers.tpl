{{- define "gc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "gc.fullname" -}}
{{- printf "registry-%s" .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "gc.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "gc.labels" -}}
helm.sh/chart: {{ include "gc.chart" . }}
{{ include "gc.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "gc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "gc.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "gc.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
{{- define "gc.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}
